# Distortioner
![Distorted cat](avatar.jpg)

Telegram bot for distorting pictures, stickers, voice messages and GIFs using Content Aware Scale.

Fork of the original Distortioner with per-user `/intensity` and admin `/update`.

## Usage
1. You'll need to install [ffmpeg](http://ffmpeg.org) and [ImageMagick](http://www.imagemagick.org/) with liquid-rescale enabled (liblqr + glib), or use Docker.
2. Create a bot with [@BotFather](https://t.me/BotFather), then set `DISTORTIONER_BOT_TOKEN`.
3. Set `DISTORTIONER_ADMIN_ID` (stats commands, `/maintenance`, `/queue`, `/update`).
4. Build with `go build` in `app/`, or use Docker below.

### User commands
- Send media / text, or reply with `/distort`
- `/intensity` — show current strength (1–100, default 50)
- `/intensity <n>` — set strength

### Admin commands
- `/daily`, `/weekly`, `/monthly` — usage stats
- `/queue` — video queue size
- `/maintenance` — toggle maintenance mode
- `/update [branch]` — pull git, rebuild image, recreate container (default branch: `wip`, override with `DISTORTIONER_UPDATE_BRANCH`)

## Docker support
Fill out `distortioner.env`, then build and run locally:

```Bash
# After pulling new code, rebuild without cache if layers stay CACHED:
docker build --no-cache -t distortioner:local .
sudo docker stop distortioner; sudo docker rm distortioner
sudo docker run -d --restart unless-stopped \
   --name distortioner \
   --env-file distortioner.env \
   -v /path/to/database/directory:/app/data \
   distortioner:local
sudo docker logs -f distortioner
```

The bot writes SQLite under `/app/data` (cwd `/app`). The volume mount must be `…:/app/data`.

Or after publishing your own image:

```Bash
sudo docker run -d --restart unless-stopped \
   --name distortioner \
   --env-file distortioner.env \
   -v /path/to/database/directory:/app/data \
   ghcr.io/xyovanz/distortioner:latest
```

### `/update` from inside Docker
The bot must see the git checkout, Docker CLI, and the daemon socket, for example:

```Bash
sudo docker run -d --restart unless-stopped \
   --name distortioner \
   --env-file distortioner.env \
   -v /path/to/database/directory:/app/data \
   -v /var/run/docker.sock:/var/run/docker.sock \
   -v /path/to/Distortioner:/src \
   -w /src \
   distortioner:local
```

Set in `distortioner.env` (or the environment):
- `DISTORTIONER_UPDATE_BRANCH=wip`
- `DISTORTIONER_IMAGE=distortioner:local`
- `DISTORTIONER_CONTAINER_NAME=distortioner`
- `DISTORTIONER_DATA_DIR=/path/to/database/directory`
- `DISTORTIONER_UPDATE_SCRIPT=/src/update.sh` (optional)
- `GITHUB_TOKEN=` (if `origin` is private HTTPS)

Manual update: `./update.sh` or `./update.sh wip`.

## What gets collected
The bot can see all group messages when privacy mode is off (needed for `/distort`). Only chat IDs and media types are stored for stats — not message text.

Temporary media files are deleted after processing. Set `DISTORTIONER_KEEP_FAILED=1` to keep files when a job fails (debugging).

## TODO
* Distort animated stickers
* Make distortion increase throughout the video
* For stickers create and maintain a separate sticker pack for ease of use
* Localization, refactoring, tests
