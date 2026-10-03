import subprocess
import os
from fastapi import FastAPI, Request, HTTPException
import hashlib, hmac
import uvicorn
import sys
import logging
import threading

import nextcloud_sync

WEBHOOK_SECRET = os.environ.get('WEBHOOK_SECRET')
if not WEBHOOK_SECRET:
    raise RuntimeError("WEBHOOK_SECRET environment variable must be set")

def verify_signature(payload_body, secret_token, signature_header):
    if not signature_header:
        raise HTTPException(status_code=403, detail='x-hub-signature-256 header is missing!')
    hash_object = hmac.new(secret_token.encode('utf-8'), msg=payload_body, digestmod=hashlib.sha256)
    expected_signature = 'sha256=' + hash_object.hexdigest()
    if not hmac.compare_digest(expected_signature, signature_header):
        raise HTTPException(status_code=403, detail='Request signatures did not match!')

app = FastAPI()

GIT_ENV = os.environ.copy()
GIT_ENV['GIT_SSH_COMMAND'] = 'ssh -i /app/keys/miniwiki -o IdentitiesOnly=yes -o StrictHostKeyChecking=no'
GIT_ENV.setdefault('GIT_AUTHOR_NAME', 'Miniwiki Nextcloud Sync')
GIT_ENV.setdefault('GIT_AUTHOR_EMAIL', 'miniwiki@localhost')
GIT_ENV.setdefault('GIT_COMMITTER_NAME', GIT_ENV['GIT_AUTHOR_NAME'])
GIT_ENV.setdefault('GIT_COMMITTER_EMAIL', GIT_ENV['GIT_AUTHOR_EMAIL'])

# Set when NEXTCLOUD_SHARE_URL is configured; owns all git updates of the wiki repo then.
syncer = None
sync_wake = threading.Event()

def pull_repo():
    if syncer:
        # Pull on the branch (not a detached HEAD) and push the new state to Nextcloud.
        sync_wake.set()
        return
    env = GIT_ENV
    cmd = ['git', 'submodule', 'update', '--init', '--recursive', '--remote']
    result = subprocess.run(cmd, capture_output=True, text=True, env=env, cwd='/app/wiki', check=True)
    if result.returncode != 0:
        raise RuntimeError(f"Command {' '.join(cmd)} failed: {result.stderr}")

@app.post('/github')
async def github_webhook(request: Request):
    payload = await request.body()
    signature = request.headers.get('X-Hub-Signature-256')
    verify_signature(payload, WEBHOOK_SECRET, signature)

    try:
        pull_repo()
    except RuntimeError as e:
        raise HTTPException(status_code=500, detail=str(e))

    return {'status': 'updated'}

if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO)
    for directory in ('/app/wiki', '/app/wiki/wiki'):
        subprocess.run(['git', 'config', '--global', '--add', 'safe.directory', directory], check=True)
    try:
        # Initial checkout of the submodule; afterwards the syncer (if any) takes over.
        subprocess.run(['git', 'submodule', 'update', '--init', '--recursive'],
                       capture_output=True, text=True, env=GIT_ENV, cwd='/app/wiki', check=True)
        syncer = nextcloud_sync.from_env('/app/wiki/wiki', GIT_ENV)
        if syncer:
            interval = float(os.environ.get('NEXTCLOUD_SYNC_INTERVAL', '60'))
            threading.Thread(target=syncer.loop, args=(interval, sync_wake), daemon=True).start()
        else:
            pull_repo()
    except subprocess.CalledProcessError as e:
        print(e)
        raise RuntimeError(f"Unable to update repository. Make sure your key is authenticated! returncode: {e.returncode} stderr: '{e.stderr}' stdout: '{e.stdout}'")
    uvicorn.run(app, host='0.0.0.0', port=9000)
