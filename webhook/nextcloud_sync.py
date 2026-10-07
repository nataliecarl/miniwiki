"""Two-way sync between a folder of the wiki git repo and a Nextcloud share link.

The sync folder (NEXTCLOUD_LOCAL_DIR, relative to the wiki repo) is the meeting point:
- edits made in Nextcloud are downloaded into the folder, committed and pushed to the repo
- edits that arrive in the folder from the repo (git pull) are uploaded to Nextcloud

Synced are `.md` and `.pdf` files anywhere (the wiki shows PDFs in its viewer and searches
their text), plus every file of any type inside a folder named `data` (at any depth). Data
folders hold attachments that articles link to with `[[file:data/<name>]]`. All of them are
committed to the repo. Each cycle is a three-way comparison of the local file, the
remote file and the last state both sides agreed on (stored in NEXTCLOUD_STATE_FILE), so a
change is propagated in whichever direction it happened. When both sides changed the same
file differently, the repo version wins and the Nextcloud version is kept next to it as
`<name> (conflict <timestamp>).<ext>`, which then syncs like any other new file.

The repo is the source of truth; Nextcloud is one more way to contribute to it. Only what
is committed (HEAD) is synced, so the working tree is settled first (see
_settle_working_tree). Every change from Nextcloud becomes a timestamped commit listing the
touched files, so a deletion there is a commit that can be reverted. A file is only deleted
in Nextcloud once its deletion is committed in the repo.

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
    """Markdown and PDF files, and any file inside a `data` folder; never hidden paths."""
    parts = rel.split("/")
    if any(p.startswith(".") for p in parts):
        return False
    # Scanners and Windows tools like to write `.PDF`; the wiki accepts any case too.
    return rel.endswith(".md") or rel.lower().endswith(".pdf") or DATA_DIR in parts[:-1]


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

    def delete_empty_dirs(self, rel: str) -> None:
        """Remove the folders above file rel that are now empty, up to the share root.

        Git has no folders, so a folder disappears from the repo with its last file; this
        mirrors that. Anything left in a folder, synced or not, keeps it (and its parents).
        """
        parts = rel.split("/")[:-1]
        while parts:
            folder = "/".join(parts)
            entries = self._propfind(folder, 1)
            if entries is None or len(entries) > 1:  # gone already, or not empty
                return
            log.info("delete remote folder %s", folder)
            self.delete(folder)
            parts.pop()

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
        """Bring the repo up to date on its branch (not a detached HEAD, so we can commit).

        Our unpushed sync commits are merged with the repo, whose side wins any conflicting
        lines; the Nextcloud edit stays in history as the merge's other parent. If git still
        can't merge (e.g. edited here, deleted there), our commits are kept on a local
        `nextcloud-sync/conflict-<timestamp>` branch and the repo's version is taken as is.
        """
        upstream = f"origin/{self.branch}"
        self.git("fetch", "origin", self.branch)
        current = self.git("rev-parse", "--abbrev-ref", "HEAD").strip()
        if current != self.branch:
            self.git("checkout", "-B", self.branch, upstream)
            return
        try:
            self.git("merge", "--no-edit", "-X", "theirs", upstream)
        except RuntimeError:
            self.git("merge", "--abort", check=False)
            backup = "nextcloud-sync/conflict-" + datetime.now().strftime("%Y%m%d-%H%M%S")
            self.git("branch", backup)
            log.warning("could not merge %s, local commits kept on branch %s", upstream, backup)
            self.git("reset", "--keep", upstream)

    def push(self) -> None:
        """Push our sync commits, if any; retried after a pull when the repo moved on."""
        for attempt in range(3):
            ahead = self.git("rev-list", "--count", f"origin/{self.branch}..HEAD").strip()
            if ahead == "0":
                return
            try:
                self.git("push", "origin", f"HEAD:{self.branch}")
                return
            except RuntimeError:
                if attempt == 2:
                    raise
                self.pull()

    def commit(self, paths: list[str], title: str = "Nextcloud sync") -> None:
        """Commit paths (relative to the sync folder), stamped and listing what changed."""
        self.git("add", "-A", "--", *[self._repo_path(p) for p in paths])
        if not self.git("diff", "--cached", "--name-only"):
            return
        stamp = datetime.now().astimezone().isoformat(timespec="seconds")
        summary = self.git("diff", "--cached", "--name-status", "--no-renames")
        self.git("commit", "-m", f"{title} {stamp}", "-m", summary)

    def _repo_path(self, rel: str) -> str:
        return f"{self.local_dir}/{rel}" if self.local_dir else rel

    def _local_tree(self) -> str:
        """Hash of the sync folder's committed tree; changes whenever any file in it does."""
        spec = f"HEAD:{self.local_dir}" if self.local_dir else "HEAD^{tree}"
        return self.git("rev-parse", "--verify", "--quiet", spec, check=False).strip()

    def _local_dirty(self) -> list[str]:
        """Synced paths (relative to the sync folder) whose working copy differs from HEAD."""
        out = self.git("status", "--porcelain", "-z", "--no-renames", "--untracked-files=all",
                       "--", self.local_dir or ".")
        prefix = self.local_dir + "/" if self.local_dir else ""
        paths = [entry[3:].removeprefix(prefix) for entry in out.split("\0") if entry]
        return [p for p in paths if is_synced_path(p)]

    def _settle_working_tree(self) -> None:
        """Make the sync folder's synced files match HEAD again, without losing anything.

        Only HEAD is synced to Nextcloud. Changes the syncer itself made but did not get to
        commit (the cycle failed in between) are committed now. Anything else was edited
        on the server by hand; it is stashed (see `git stash list`) rather than synced.
        """
        known = self.state["files"]
        ours, stray = [], []
        for rel in self._local_dirty():
            path = os.path.join(self.root, rel)
            if os.path.isfile(path):
                mine = rel in known and blob_sha(self._read_local(rel)) == known[rel]["sha"]
            else:
                mine = rel not in known
            (ours if mine else stray).append(rel)
        if ours:
            self.commit(ours)
        if stray:
            log.warning("stashing manual edits in the sync folder: %s", ", ".join(stray))
            self.git("stash", "push", "--include-untracked", "-m",
                     "Manual edits set aside by Nextcloud sync", "--",
                     *[self._repo_path(p) for p in stray])

    def _local_files(self) -> dict[str, str]:
        """Return {path: blob hash} of the synced files in HEAD, read from git's index."""
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
        return files

    def _deletion_committed(self, rel: str) -> bool:
        """Whether rel (absent from HEAD) is in HEAD's history, so git can bring it back."""
        return bool(self.git("rev-list", "-1", "HEAD", "--", self._repo_path(rel)).strip())

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
        # Like git, drop the folders the file leaves empty (rmdir refuses non-empty ones).
        folder = os.path.dirname(rel)
        while folder:
            try:
                os.rmdir(os.path.join(self.root, folder))
            except OSError:
                break
            folder = os.path.dirname(folder)

    # --- sync --------------------------------------------------------------------------

    def run_once(self) -> None:
        with self.lock:
            self._settle_working_tree()
            self.pull()
            self.push()
            root_etag = self.share.etag("")
            if root_etag is None:
                raise RuntimeError("Nextcloud share not found (wrong link or password?)")
            if (self._local_tree() == self.state["tree"]
                    and root_etag == self.state["root_etag"]):
                return  # nothing changed on either side

            remote_dirs, remote = self.share.list_files(
                self.state["dirs"], {f: s["etag"] for f, s in self.state["files"].items()})
            failed = False
            try:
                changed_locally, failed = self._reconcile(self._local_files(), remote)
                if changed_locally:
                    self.commit(changed_locally)
                    self.push()
                # Etags seen before our own uploads: those uploads make the next cycle list
                # the touched folders once more, then it settles. After a failure, force a
                # full comparison next time so the failed file is retried.
                self.state["dirs"] = remote_dirs
                self.state["root_etag"] = "" if failed else remote_dirs.get("", root_etag)
                self.state["tree"] = "" if failed else self._local_tree()
            finally:
                self._save_state()

    def _reconcile(self, local: dict[str, str], remote: dict[str, str]
                   ) -> tuple[list[str], bool]:
        known = self.state["files"]
        changed_locally: list[str] = []
        failed = False

        for rel in sorted(set(local) | set(remote) | set(known)):
            if not is_synced_path(rel):
                continue  # never touch anything else, whatever the inputs say
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
                elif (remote_sha == base_sha and local_sha is None
                      and not self._deletion_committed(rel)):
                    # Not in the repo, but never deleted there either (e.g. a stale state
                    # file or a changed NEXTCLOUD_LOCAL_DIR): bring it back, don't delete.
                    log.warning("not deleting remote %s, the repo never had it; "
                                "adding it to the repo instead", rel)
                    if remote_data is None:
                        remote_data = self.share.download(rel)
                    self._write_local(rel, remote_data)
                    changed_locally.append(rel)
                    synced = (remote_sha, remote_etag)
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
                    self.share.delete_empty_dirs(rel)
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
