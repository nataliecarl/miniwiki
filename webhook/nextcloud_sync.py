"""Two-way sync between a folder of the wiki git repo and a Nextcloud share link.

The sync folder (NEXTCLOUD_LOCAL_DIR, relative to the wiki repo) is the meeting point:
- edits made in Nextcloud are downloaded into the folder, committed and pushed to the repo
- edits that arrive in the folder from the repo (git pull) are uploaded to Nextcloud

Synced are `.md` files, plus every file of any type inside a folder named `data` (at any
depth). Data folders hold attachments that articles link to with `[[file:data/<name>]]`.
Both kinds are committed to the repo. Each cycle is a three-way comparison of the local file, the
remote file and the last state both sides agreed on (stored in NEXTCLOUD_STATE_FILE), so a
change is propagated in whichever direction it happened. When both sides changed the same
file differently, the repo version wins and the Nextcloud version is kept next to it as
`<name> (conflict <timestamp>).<ext>`, which then syncs like any other new file.

Change detection is hash based, so an idle cycle is one WebDAV request plus a few git calls:
- Content is identified by its git blob hash. Local hashes come from `git ls-tree` without
  reading files; only paths that `git status` reports as changed are hashed from disk.
- Nextcloud folder etags change whenever anything below them changes. If the share's root
  etag and the sync folder's git tree hash both match the last sync, the cycle stops there.
  Otherwise only folders whose etag changed are listed, and a file is only downloaded when
  its etag changed.
"""

import hashlib
import json
import logging
import os
import subprocess
import threading
import xml.etree.ElementTree as ET
from datetime import datetime
from urllib.parse import quote, unquote, urlsplit

import httpx

log = logging.getLogger("nextcloud-sync")

DAV_NS = {"d": "DAV:"}
PROPFIND_BODY = (
    '<?xml version="1.0"?>'
    '<d:propfind xmlns:d="DAV:"><d:prop>'
    "<d:getetag/><d:resourcetype/>"
    "</d:prop></d:propfind>"
)


def blob_sha(data: bytes) -> str:
    """The hash git gives this content, so local and remote hashes are comparable."""
    return hashlib.sha1(b"blob %d\0" % len(data) + data).hexdigest()


DATA_DIR = "data"


def is_synced_path(rel: str) -> bool:
    """Markdown files, and any file inside a `data` folder; never hidden paths."""
    parts = rel.split("/")
    if any(p.startswith(".") for p in parts):
        return False
    return rel.endswith(".md") or DATA_DIR in parts[:-1]


def under(prefix: str, rel: str) -> bool:
    return rel.startswith(prefix + "/")


class NextcloudShare:
    """WebDAV access to a public Nextcloud share link (https://host/s/<token>)."""

    def __init__(self, share_url: str, password: str = ""):
        parts = urlsplit(share_url.rstrip("/"))
        token = parts.path.rsplit("/s/", 1)[-1]
        if not token or "/" in token:
            raise ValueError(f"Not a Nextcloud share link: {share_url}")
        # Everything before /s/ (e.g. a subpath install or /index.php) is the instance root.
        base_path = parts.path.rsplit("/s/", 1)[0].removesuffix("/index.php")
        self.base = f"{parts.scheme}://{parts.netloc}{base_path}/public.php/webdav"
        self.base_path = urlsplit(self.base).path
        self.client = httpx.Client(
            auth=(token, password),
            headers={"X-Requested-With": "XMLHttpRequest"},
            timeout=60,
        )

    def _url(self, rel: str) -> str:
        return f"{self.base}/{quote(rel)}" if rel else f"{self.base}/"

    def _propfind(self, rel: str, depth: int) -> list[tuple[str, bool, str]] | None:
        """Return [(relative path, is folder, etag)] for rel (and its children at depth 1)."""
        resp = self.client.request(
            "PROPFIND", self._url(rel), content=PROPFIND_BODY, headers={"Depth": str(depth)}
        )
        if resp.status_code == 404:
            return None
        resp.raise_for_status()
        entries = []
        for node in ET.fromstring(resp.content).findall("d:response", DAV_NS):
            href = unquote(urlsplit(node.findtext("d:href", "", DAV_NS)).path)
            is_dir = node.find(".//d:resourcetype/d:collection", DAV_NS) is not None
            etag = node.findtext(".//d:getetag", "", DAV_NS).strip('"')
            entries.append((href.removeprefix(self.base_path).strip("/"), is_dir, etag))
        return entries

    def etag(self, rel: str) -> str | None:
        entries = self._propfind(rel, 0)
        return entries[0][2] if entries else None

    def list_files(self, known_dirs: dict[str, str], known_files: dict[str, str]
                   ) -> tuple[dict[str, str], dict[str, str]]:
        """Return ({folder: etag}, {file: etag}) for the share.

        Folders whose etag is unchanged since known_dirs are not listed again; their
        contents are taken from known_dirs/known_files instead.
        """
        dirs: dict[str, str] = {}
        files: dict[str, str] = {}
        pending = [""]
        while pending:
            folder = pending.pop()
            for rel, is_dir, etag in self._propfind(folder, 1) or []:
                if rel == folder:
                    dirs[rel] = etag
                elif is_dir:
                    if rel.split("/")[-1].startswith("."):
                        continue
                    if known_dirs.get(rel) == etag:
                        dirs.update({d: e for d, e in known_dirs.items() if d == rel or under(rel, d)})
                        files.update({f: e for f, e in known_files.items() if under(rel, f)})
                    else:
                        pending.append(rel)
                elif is_synced_path(rel):
                    files[rel] = etag
        return dirs, files

    def download(self, rel: str) -> bytes:
        resp = self.client.get(self._url(rel))
        resp.raise_for_status()
        return resp.content

    def upload(self, rel: str, data: bytes) -> str | None:
        """Upload rel and return its new etag."""
        self._make_dirs(rel)
        resp = self.client.put(self._url(rel), content=data)
        resp.raise_for_status()
        etag = resp.headers.get("OC-ETag") or resp.headers.get("ETag")
        return etag.strip('"') if etag else self.etag(rel)

    def delete(self, rel: str) -> None:
        resp = self.client.delete(self._url(rel))
        if resp.status_code != 404:
            resp.raise_for_status()

    def _make_dirs(self, rel: str) -> None:
        parts = rel.split("/")[:-1]
        for i in range(1, len(parts) + 1):
            resp = self.client.request("MKCOL", self._url("/".join(parts[:i])))
            # 405: the folder already exists
            if resp.status_code not in (201, 405):
                resp.raise_for_status()


class Syncer:
    def __init__(self, repo: str, local_dir: str, share: NextcloudShare, state_file: str,
                 branch: str, git_env: dict[str, str]):
        self.repo = repo
        self.local_dir = local_dir.strip("/")
        self.root = os.path.join(repo, self.local_dir) if self.local_dir else repo
        self.share = share
        self.state_file = state_file
        self.branch = branch
        self.git_env = git_env
        self.lock = threading.Lock()
        self.state = self._load_state()

    # --- git ---------------------------------------------------------------------------

    def git(self, *args: str, check: bool = True) -> str:
        # Only content is synced; permission bit changes must not count as edits.
        result = subprocess.run(["git", "-c", "core.fileMode=false", *args], cwd=self.repo,
                                env=self.git_env, capture_output=True, text=True)
        if check and result.returncode != 0:
            raise RuntimeError(f"git {' '.join(args)} failed: {result.stderr.strip()}")
        return result.stdout

    def pull(self) -> None:
        """Bring the repo up to date on its branch (not a detached HEAD, so we can commit)."""
        self.git("fetch", "origin", self.branch)
        current = self.git("rev-parse", "--abbrev-ref", "HEAD").strip()
        if current != self.branch:
            self.git("checkout", "-B", self.branch, f"origin/{self.branch}")
        else:
            self.git("rebase", "--autostash", f"origin/{self.branch}")

    def commit_and_push(self, paths: list[str]) -> None:
        self.git("add", "-A", "--", *[self._repo_path(p) for p in paths])
        if not self.git("diff", "--cached", "--name-only"):
            return
        self.git("commit", "-m", f"Sync {len(paths)} file(s) from Nextcloud")
        for attempt in range(3):
            try:
                self.git("push", "origin", f"HEAD:{self.branch}")
                return
            except RuntimeError:
                if attempt == 2:
                    raise
                self.pull()

    def _repo_path(self, rel: str) -> str:
        return f"{self.local_dir}/{rel}" if self.local_dir else rel

    def _local_tree(self) -> str:
        """Hash of the sync folder's committed tree; changes whenever any file in it does."""
        spec = f"HEAD:{self.local_dir}" if self.local_dir else "HEAD^{tree}"
        return self.git("rev-parse", "--verify", "--quiet", spec, check=False).strip()

    def _local_dirty(self) -> list[str]:
        """Paths (relative to the sync folder) whose working copy differs from HEAD."""
        out = self.git("status", "--porcelain", "-z", "--no-renames", "--untracked-files=all",
                       "--", self.local_dir or ".")
        prefix = self.local_dir + "/" if self.local_dir else ""
        return [entry[3:].removeprefix(prefix) for entry in out.split("\0") if entry]

    def _local_files(self, dirty: list[str]) -> dict[str, str]:
        """Return {path: blob hash}, from git's index of HEAD plus any dirty files on disk."""
        files: dict[str, str] = {}
        prefix = self.local_dir + "/" if self.local_dir else ""
        out = self.git("ls-tree", "-r", "-z", "HEAD", "--", self.local_dir or ".")
        for entry in out.split("\0"):
            if not entry:
                continue
            meta, path = entry.split("\t", 1)
            rel = path.removeprefix(prefix)
            if meta.split()[1] == "blob" and is_synced_path(rel):
                files[rel] = meta.split()[2]
        for rel in dirty:
            files.pop(rel, None)
            if is_synced_path(rel) and os.path.isfile(os.path.join(self.root, rel)):
                files[rel] = blob_sha(self._read_local(rel))
        return files

    # --- state -------------------------------------------------------------------------

    def _load_state(self) -> dict:
        state = {"tree": "", "root_etag": "", "dirs": {}, "files": {}}
        try:
            with open(self.state_file) as f:
                state.update(json.load(f))
        except FileNotFoundError:
            pass
        return state

    def _save_state(self) -> None:
        os.makedirs(os.path.dirname(self.state_file) or ".", exist_ok=True)
        tmp = self.state_file + ".tmp"
        with open(tmp, "w") as f:
            json.dump(self.state, f, indent=1, sort_keys=True)
        os.replace(tmp, self.state_file)

    # --- local files -------------------------------------------------------------------

    def _read_local(self, rel: str) -> bytes:
        with open(os.path.join(self.root, rel), "rb") as f:
            return f.read()

    def _write_local(self, rel: str, data: bytes) -> None:
        path = os.path.join(self.root, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        # Atomic replace so the wiki server never reads a half-written file.
        tmp = os.path.join(os.path.dirname(path), f".{os.path.basename(path)}.sync-tmp")
        with open(tmp, "wb") as f:
            f.write(data)
        os.replace(tmp, path)

    def _delete_local(self, rel: str) -> None:
        try:
            os.remove(os.path.join(self.root, rel))
        except FileNotFoundError:
            pass

    # --- sync --------------------------------------------------------------------------

    def run_once(self) -> None:
        with self.lock:
            self.pull()
            root_etag = self.share.etag("")
            if root_etag is None:
                raise RuntimeError("Nextcloud share not found (wrong link or password?)")
            tree = self._local_tree()
            dirty = self._local_dirty()
            if (not dirty and tree == self.state["tree"]
                    and root_etag == self.state["root_etag"]):
                return  # nothing changed on either side

            remote_dirs, remote = self.share.list_files(
                self.state["dirs"], {f: s["etag"] for f, s in self.state["files"].items()})
            failed = False
            try:
                changed_locally, failed = self._reconcile(self._local_files(dirty), remote)
                if changed_locally:
                    self.commit_and_push(changed_locally)
                # Etags seen before our own uploads: those uploads make the next cycle list
                # the touched folders once more, then it settles. After a failure, force a
                # full comparison next time so the failed file is retried.
                self.state["dirs"] = remote_dirs
                self.state["root_etag"] = "" if failed else remote_dirs.get("", root_etag)
                self.state["tree"] = "" if failed else self._local_tree()
            finally:
                self._save_state()

    def _reconcile(self, local: dict[str, str], remote: dict[str, str]) -> tuple[list[str], bool]:
        known = self.state["files"]
        changed_locally: list[str] = []
        failed = False

        for rel in sorted(set(local) | set(remote) | set(known)):
            base = known.get(rel)
            base_sha = base["sha"] if base else None
            local_sha = local.get(rel)
            remote_etag = remote.get(rel)

            try:
                remote_data = None
                if remote_etag is None:
                    remote_sha = None
                elif base and remote_etag == base["etag"]:
                    remote_sha = base_sha
                else:
                    # Etag changed: fetch it to see whether the content really did.
                    remote_data = self.share.download(rel)
                    remote_sha = blob_sha(remote_data)

                if local_sha == remote_sha:
                    synced = (local_sha, remote_etag)  # in sync (or gone on both sides)
                elif local_sha == base_sha or local_sha is None and remote_sha != base_sha:
                    # Only Nextcloud changed, or it was edited there and deleted in the repo
                    # (the edit wins) -> apply to the repo.
                    if remote_sha is None:
                        log.info("delete local %s", rel)
                        self._delete_local(rel)
                    else:
                        log.info("download %s", rel)
                        self._write_local(rel, remote_data)
                    changed_locally.append(rel)
                    synced = (remote_sha, remote_etag)
                elif remote_sha == base_sha and local_sha is None:
                    log.info("delete remote %s", rel)
                    self.share.delete(rel)
                    synced = (None, None)
                else:
                    if remote_sha not in (None, base_sha):
                        # Edited on both sides: repo wins, Nextcloud's version is kept.
                        stamp = datetime.now().strftime("%Y-%m-%d %H%M%S")
                        stem, ext = os.path.splitext(rel)
                        conflict = f"{stem} (conflict {stamp}){ext}"
                        log.warning("conflict on %s, Nextcloud version saved as %s", rel, conflict)
                        self.share.upload(conflict, remote_data)
                    # Only the repo changed, or it was edited here and deleted in Nextcloud.
                    log.info("upload %s", rel)
                    synced = (local_sha, self.share.upload(rel, self._read_local(rel)))
            except Exception:
                log.exception("failed to sync %s", rel)
                failed = True
                continue

            sha, etag = synced
            if sha is None or etag is None:
                known.pop(rel, None)
            else:
                known[rel] = {"sha": sha, "etag": etag}
        return changed_locally, failed

    def loop(self, interval: float, wake: threading.Event) -> None:
        while True:
            try:
                self.run_once()
            except Exception:
                log.exception("Nextcloud sync cycle failed")
            wake.wait(interval)
            wake.clear()


def from_env(repo: str, git_env: dict[str, str]) -> Syncer | None:
    share_url = os.environ.get("NEXTCLOUD_SHARE_URL")
    if not share_url:
        return None
    share = NextcloudShare(share_url, os.environ.get("NEXTCLOUD_SHARE_PASSWORD", ""))
    return Syncer(
        repo=repo,
        local_dir=os.environ.get("NEXTCLOUD_LOCAL_DIR", ""),
        share=share,
        state_file=os.environ.get("NEXTCLOUD_STATE_FILE", "/app/state/nextcloud-sync.json"),
        branch=os.environ.get("WIKI_BRANCH", "master"),
        git_env=git_env,
    )
