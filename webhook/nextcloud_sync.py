"""Two-way sync between a folder of the wiki git repo and a Nextcloud share link.

The sync folder (NEXTCLOUD_LOCAL_DIR, relative to the wiki repo) is the meeting point:
- edits made in Nextcloud are downloaded into the folder, committed and pushed to the repo
- edits that arrive in the folder from the repo (git pull) are uploaded to Nextcloud

Synced are `.md` files anywhere, plus every file of any type inside a folder named
`templates` (at any depth). Templates folders hold templates and attachments, PDFs included,
that articles link to with `[[file:templates/<name>]]`. All of them are committed to the repo. Nothing else is synced: a
shared folder may hold far more (e.g. Office documents) than belongs in git.

Each cycle is a three-way comparison of the local file, the remote file and the last state
both sides agreed on (stored in NEXTCLOUD_STATE_FILE), so a change is propagated in
whichever direction it happened. When both sides changed the same
file differently, the repo version wins and the Nextcloud version is kept next to it as
`<name> (conflict <timestamp>).<ext>`, which then syncs like any other new file.

Files are matched across the two sides by their path ignoring case, since Nextcloud's
desktop clients on Windows and macOS can't keep `Foo.md` and `foo.md` apart. Names are
still synced exactly as written: a rename that only changes case is a rename of the same
file. Names that differ only in case on the same side are a clash; they are left alone on
both sides (and logged) until one of them is renamed.

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
  Otherwise only folders whose etag changed are listed (see NextcloudShare.list_files), and
  a file is only downloaded when its etag changed.

A large share takes thousands of requests to list the first time. Failed requests are
retried, and the listing is saved as it goes, so an interrupted first sync resumes where it
stopped instead of starting over. A folder the server keeps timing out on is skipped (its
files are left alone on both sides) and retried in the background with a 10 minute
timeout; if that fails too, it stays skipped until it changes in Nextcloud.

To start over, create a file named `reset` next to the state file: the next cycle drops all
state. (Deleting the state file does not work while the sync runs; it is written back.)
"""

import hashlib
import json
import logging
import os
import subprocess
import threading
import time
import xml.etree.ElementTree as ET
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
from typing import Callable
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


TEMPLATES_DIR = "templates"


def is_synced_path(rel: str) -> bool:
    """Markdown files, and any file inside a `templates` folder; never hidden paths."""
    parts = rel.split("/")
    if any(p.startswith(".") for p in parts):
        return False
    return rel.endswith(".md") or TEMPLATES_DIR in parts[:-1]


def path_key(rel: str) -> str:
    """What identifies a file on both sides: its path, ignoring case."""
    return rel.lower()


def group_by_key(files: dict[str, str], side: str) -> tuple[dict[str, tuple[str, str]], set[str]]:
    """Return ({key: (path, value)}, clashing keys) for {path: value}.

    A key spelled more than once on one side (`Foo.md` next to `foo.md`) clashes.
    """
    grouped: dict[str, tuple[str, str]] = {}
    spellings: dict[str, list[str]] = {}
    for rel, value in files.items():
        key = path_key(rel)
        grouped[key] = (rel, value)
        spellings.setdefault(key, []).append(rel)
    clashes = {key for key, names in spellings.items() if len(names) > 1}
    for key in sorted(clashes):
        log.warning("not syncing %s: names differ only in case in %s",
                    ", ".join(sorted(spellings[key])), side)
    return grouped, clashes


def in_folders(rel: str, folders: set[str]) -> bool:
    """Whether rel is one of folders or lies below one, ignoring case ("" is the root)."""
    key = path_key(rel)
    return any(f == "" or key == path_key(f) or key.startswith(path_key(f) + "/")
               for f in folders)


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
        self.slow_pool = ThreadPoolExecutor(max_workers=1, thread_name_prefix="nextcloud-slow")
        self.slow_lock = threading.Lock()
        self.slow_pending = set()
        self.slow_results = {}
        self.on_slow_result: Callable[[], None] | None = None

    def _url(self, rel: str) -> str:
        return f"{self.base}/{quote(rel)}" if rel else f"{self.base}/"

    # Retries for a request that times out or meets an overloaded server. A folder listing
    # that still fails is handed to the slow path instead (see list_files).
    RETRY_DELAYS: tuple[int, ...] = (5, 30)
    SLOW_TIMEOUT = httpx.Timeout(60, read=600)
    SLOW_RETRY_DELAYS: tuple[int, ...] = (60,)

    def _request(self, method: str, rel: str, retry_delays: tuple[int, ...] | None = None,
                 **kwargs) -> httpx.Response:
        """Send a request, retrying timeouts, connection errors and server overload.

        Every request the sync makes is safe to repeat: a PUT or DELETE that did go through
        the first time has the same result again, and a MOVE that did shows up as a 404
        (handled like any other failure of that file).
        """
        delays = self.RETRY_DELAYS if retry_delays is None else retry_delays
        for delay in (*delays, None):
            try:
                resp = self.client.request(method, self._url(rel), **kwargs)
                if resp.status_code < 500 and resp.status_code != 429:
                    return resp
                problem = f"HTTP {resp.status_code}"
            except httpx.TransportError as e:
                if delay is None:
                    raise
                problem = repr(e)
            if delay is None:
                return resp
            log.warning("%s %s failed (%s), retrying in %ds", method, rel or "/", problem, delay)
            time.sleep(delay)
        raise AssertionError("unreachable")

    def _propfind(self, rel: str, depth: int, **kwargs) -> list[tuple[str, bool, str]] | None:
        """Return [(relative path, is folder, etag)] for rel (and its children at depth 1)."""
        resp = self._request("PROPFIND", rel, content=PROPFIND_BODY,
                             headers={"Depth": str(depth)}, **kwargs)
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

    def list_files(self, root_etag: str, listing: dict[str, dict], skipped: dict[str, str],
                   save: Callable[[], None], save_every: float = 30
                   ) -> tuple[dict[str, str], set[str]]:
        """Return ({file: etag} of the synced files in the share, folders skipped).

        listing caches each folder's contents as {folder: {"etag": its etag, "dirs":
        {subfolder: etag}, "files": {synced file: etag}}} and is updated in place. A folder
        whose etag still matches its cached one is taken from the cache without a request;
        the others are listed. Every folder's etag is known from its parent's listing
        before it is visited, so an unchanged share costs no requests at all, and one
        change costs only the folders on its path.

        The cache only ever holds complete listings of single folders, so it can be saved
        while the walk is under way (via save, every save_every seconds and when the walk
        fails): an interrupted walk resumes from it, listing only what it had not reached.

        A folder that can't be listed (the server keeps timing out on it) is skipped, with
        everything below it, and retried in the background with a much longer timeout; a
        later walk picks up the result. If that fails too, the folder is recorded in
        skipped ({folder: etag}) and left out until its etag changes. The caller must leave
        files below skipped folders alone: their absence here says nothing.
        """
        self._collect_slow_results(listing, skipped)
        files: dict[str, str] = {}
        seen: set[str] = set()
        skipped_now: set[str] = set()
        pending = [("", root_etag)]
        last_save = time.monotonic()
        try:
            while pending:
                folder, etag = pending.pop()
                seen.add(folder)
                entry = listing.get(folder)
                if entry is None or entry["etag"] != etag:
                    if skipped.get(folder) == etag or folder in self.slow_pending:
                        skipped_now.add(folder)
                        continue
                    skipped.pop(folder, None)  # changed since it was given up on: try again
                    try:
                        entry = self._list_folder(folder)
                    except (httpx.TransportError, httpx.HTTPStatusError) as e:
                        if isinstance(e, httpx.HTTPStatusError) and e.response.status_code < 500:
                            raise
                        log.warning("skipping %s for now (%r), retrying it in the background "
                                    "with a %ds timeout", folder or "/", e, self.SLOW_TIMEOUT.read)
                        self._list_slowly(folder, etag)
                        skipped_now.add(folder)
                        continue
                    if entry is None:
                        listing.pop(folder, None)
                        continue  # deleted since its parent was listed
                    listing[folder] = entry
                    if time.monotonic() - last_save > save_every:
                        save()
                        last_save = time.monotonic()
                files.update(entry["files"])
                pending.extend(entry["dirs"].items())
        except BaseException:
            save()
            raise
        # Drop what is gone from the share; below a skipped folder nothing is known.
        for folder in set(listing) - seen:
            if not in_folders(folder, skipped_now):
                del listing[folder]
        for folder in set(skipped) - seen:
            if not in_folders(folder, skipped_now):
                del skipped[folder]
        return files, skipped_now

    def _list_folder(self, folder: str, **kwargs) -> dict | None:
        """One folder's cache entry for list_files, or None if it does not exist."""
        entries = self._propfind(folder, 1, **kwargs)
        if entries is None:
            return None
        entry: dict = {"etag": "", "dirs": {}, "files": {}}
        for rel, is_dir, etag in entries:
            if rel == folder:
                entry["etag"] = etag
            elif is_dir:
                if not rel.split("/")[-1].startswith("."):
                    entry["dirs"][rel] = etag
            elif is_synced_path(rel):
                entry["files"][rel] = etag
        return entry

    # --- slow path: folders the normal walk could not list ------------------------------

    slow_pending: set[str]
    slow_results: dict[str, tuple[str, bool, dict | None]]

    def _list_slowly(self, folder: str, etag: str) -> None:
        """List folder in the background; its result goes to slow_results."""
        with self.slow_lock:
            self.slow_pending.add(folder)
        self.slow_pool.submit(self._list_slowly_now, folder, etag)

    def _list_slowly_now(self, folder: str, etag: str) -> None:
        try:
            entry = self._list_folder(folder, timeout=self.SLOW_TIMEOUT,
                                      retry_delays=self.SLOW_RETRY_DELAYS)
            log.info("listed %s in the background", folder or "/")
            result = (etag, True, entry)
        except Exception as e:
            log.warning("giving up on %s (%r): it stays skipped until it changes in Nextcloud",
                        folder or "/", e)
            result = (etag, False, None)
        with self.slow_lock:
            self.slow_pending.discard(folder)
            self.slow_results[folder] = result
        if self.on_slow_result:
            self.on_slow_result()

    def _collect_slow_results(self, listing: dict[str, dict], skipped: dict[str, str]) -> None:
        with self.slow_lock:
            results, self.slow_results = self.slow_results, {}
        for folder, (etag, ok, entry) in results.items():
            if not ok:
                skipped[folder] = etag
            elif entry is None:
                listing.pop(folder, None)
            else:
                listing[folder] = entry

    def forget_slow_results(self) -> None:
        with self.slow_lock:
            self.slow_results = {}

    def download(self, rel: str) -> bytes:
        resp = self._request("GET", rel)
        resp.raise_for_status()
        return resp.content

    def upload(self, rel: str, data: bytes) -> str | None:
        """Upload rel and return its new etag."""
        self._make_dirs(rel)
        resp = self._request("PUT", rel, content=data)
        resp.raise_for_status()
        etag = resp.headers.get("OC-ETag") or resp.headers.get("ETag")
        return etag.strip('"') if etag else self.etag(rel)

    def delete(self, rel: str) -> None:
        resp = self._request("DELETE", rel)
        if resp.status_code != 404:
            resp.raise_for_status()

    def move(self, src: str, dst: str) -> None:
        """Rename src to dst, keeping the file's Nextcloud history and shares."""
        self._make_dirs(dst)
        resp = self._request("MOVE", src, headers={"Destination": self._url(dst), "Overwrite": "F"})
        resp.raise_for_status()
        self.delete_empty_dirs(src)

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
            resp = self._request("MKCOL", "/".join(parts[:i]))
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
            entry = known.get(path_key(rel))
            if os.path.isfile(os.path.join(self.root, rel)):
                mine = (entry is not None and entry["name"] == rel
                        and blob_sha(self._read_local(rel)) == entry["sha"])
            else:
                mine = entry is None or entry["name"] != rel
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

    def _load_state(self, fresh: bool = False) -> dict:
        state = {"tree": "", "root_etag": "", "listing": {}, "skipped": {}, "files": {}}
        if fresh:
            return state
        try:
            with open(self.state_file) as f:
                state.update(json.load(f))
        except FileNotFoundError:
            pass
        state.pop("dirs", None)  # replaced by "listing"; a missing listing is rebuilt
        # Files are keyed by path_key; older state files were keyed by the name itself.
        state["files"] = {path_key(s.get("name", rel)): {"name": rel, **s}
                          for rel, s in state["files"].items()}
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
        self._prune_empty_dirs(rel)

    def _rename_local(self, old: str, new: str) -> None:
        path = os.path.join(self.root, new)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        os.rename(os.path.join(self.root, old), path)
        self._prune_empty_dirs(old)

    def _prune_empty_dirs(self, rel: str) -> None:
        # Like git, drop the folders file rel left empty (rmdir refuses non-empty ones).
        folder = os.path.dirname(rel)
        while folder:
            try:
                os.rmdir(os.path.join(self.root, folder))
            except OSError:
                break
            folder = os.path.dirname(folder)

    # --- sync --------------------------------------------------------------------------

    def reset_requested(self) -> bool:
        """Consume the reset trigger: a file named `reset` next to the state file."""
        trigger = os.path.join(os.path.dirname(self.state_file) or ".", "reset")
        try:
            os.remove(trigger)
        except FileNotFoundError:
            return False
        return True

    def run_once(self) -> None:
        with self.lock:
            if self.reset_requested():
                # The running process holds the state in memory, so deleting the state file
                # by hand would not do: it would be written back. This starts over cleanly.
                log.warning("state reset requested: forgetting all sync state, starting over")
                self.state = self._load_state(fresh=True)
                self.share.forget_slow_results()
                self._save_state()
            self._settle_working_tree()
            self.pull()
            self.push()
            root_etag = self.share.etag("")
            if root_etag is None:
                raise RuntimeError("Nextcloud share not found (wrong link or password?)")
            if (self._local_tree() == self.state["tree"]
                    and root_etag == self.state["root_etag"]):
                return  # nothing changed on either side

            listing = self.state["listing"]
            remote, skipped = self.share.list_files(root_etag, listing, self.state["skipped"],
                                                    self._save_state)
            failed = False
            try:
                changed_locally, failed = self._reconcile(self._local_files(), remote, skipped)
                # Skipped folders still need syncing: keep comparing until they are done.
                failed = failed or bool(skipped)
                if changed_locally:
                    self.commit(changed_locally)
                    self.push()
                # The listing holds etags seen before our own uploads: those uploads make the
                # next cycle list the touched folders once more, then it settles. After a
                # failure, force a comparison next time so the failed file is retried.
                self.state["root_etag"] = "" if failed else listing[""]["etag"]
                self.state["tree"] = "" if failed else self._local_tree()
            finally:
                self._save_state()

    def _reconcile(self, local: dict[str, str], remote: dict[str, str],
                   skipped: set[str] = frozenset()) -> tuple[list[str], bool]:
        """Compare both sides file by file, matching names ignoring case.

        A name that differs only in case is the same file, and the name itself is synced:
        the side that renamed it wins, the repo if both did. Names that differ only in case
        on the same side clash and are left alone on both sides until one is renamed.
        Files below folders that could not be listed (skipped) are left alone too: they are
        missing from remote, which must not read as deleted in Nextcloud.
        """
        known = self.state["files"]
        changed_locally: list[str] = []
        failed = False
        local_by_key, local_clashes = group_by_key(local, "the repo")
        remote_by_key, remote_clashes = group_by_key(remote, "Nextcloud")
        clashes = local_clashes | remote_clashes

        for key in sorted(set(local_by_key) | set(remote_by_key) | set(known)):
            if key in clashes:
                continue
            local_name, local_sha = local_by_key.get(key, (None, None))
            remote_name, remote_etag = remote_by_key.get(key, (None, None))
            base = known.get(key)
            base_sha = base["sha"] if base else None
            base_name = base["name"] if base else None
            names = [n for n in (local_name, remote_name, base_name) if n]
            if not all(is_synced_path(n) for n in names):
                continue  # never touch anything else, whatever the inputs say
            if skipped and any(in_folders(n, skipped) for n in names):
                continue
            rel = local_name or remote_name or base_name

            try:
                remote_data = None
                if remote_etag is None:
                    remote_sha = None
                elif base and remote_etag == base["etag"]:
                    remote_sha = base_sha
                else:
                    # Etag changed: fetch it to see whether the content really did.
                    remote_data = self.share.download(remote_name)
                    remote_sha = blob_sha(remote_data)

                renamed_here = local_name is not None and local_name != base_name
                renamed_there = remote_name is not None and remote_name != base_name
                if local_name and remote_name and local_name != remote_name:
                    if renamed_there and not renamed_here:
                        log.info("rename local %s -> %s", local_name, remote_name)
                        self._rename_local(local_name, remote_name)
                        changed_locally += [local_name, remote_name]
                        rel = remote_name
                    else:
                        log.info("rename remote %s -> %s", remote_name, rel)
                        self.share.move(remote_name, rel)
                # Against a deletion on the other side, a rename counts as an edit.
                local_changed = local_sha != base_sha or renamed_here and remote_sha is None
                remote_changed = remote_sha != base_sha or renamed_there and local_sha is None

                if local_sha == remote_sha:
                    synced = (local_sha, remote_etag)  # in sync (or gone on both sides)
                elif (remote_sha == base_sha and local_sha is None
                      and not any(self._deletion_committed(n) for n in {rel, base_name} if n)):
                    # Not in the repo, but never deleted there either (e.g. a stale state
                    # file or a changed NEXTCLOUD_LOCAL_DIR): bring it back, don't delete.
                    log.warning("not deleting remote %s, the repo never had it; "
                                "adding it to the repo instead", rel)
                    if remote_data is None:
                        remote_data = self.share.download(rel)
                    self._write_local(rel, remote_data)
                    changed_locally.append(rel)
                    synced = (remote_sha, remote_etag)
                elif not local_changed or local_sha is None and remote_changed:
                    # Only Nextcloud changed, or it was edited there and deleted in the repo
                    # (the edit wins) -> apply to the repo.
                    if remote_sha is None:
                        log.info("delete local %s", rel)
                        self._delete_local(rel)
                    else:
                        log.info("download %s", rel)
                        if remote_data is None:
                            remote_data = self.share.download(rel)
                        self._write_local(rel, remote_data)
                    changed_locally.append(rel)
                    synced = (remote_sha, remote_etag)
                elif not remote_changed and local_sha is None:
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
                known.pop(key, None)
            else:
                known[key] = {"name": rel, "sha": sha, "etag": etag}
        return changed_locally, failed

    def loop(self, interval: float, wake: threading.Event) -> None:
        # A folder listed in the background is picked up by the next cycle; start it now.
        self.share.on_slow_result = wake.set
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
