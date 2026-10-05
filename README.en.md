# OpenIPC NVR — video surveillance server with AI detection and access control

**Read in another language:**
[Русский](README.md) · [简体中文](README.zh-CN.md) · [한국어](README.ko.md)

A video surveillance server for distributed IP cameras (OpenIPC, Hikvision, Dahua, Vivotek,
ONVIF) with neural-network detection of objects and sounds, face and licence plate
recognition, access control integration and a web interface.

```
┌──────────────┐  RTSP   ┌────────────┐  MSE/WebRTC  ┌──────────────┐
│   Cameras    │────────▶│  go2rtc    │─────────────▶│  Web UI      │
│ OpenIPC/ONVIF│         │(media server)│             │  React SPA   │
└──────────────┘         └─────┬──────┘              └──────┬───────┘
                               │                            │ REST
┌──────────────┐   NATS  ┌─────▼────────────────────────────▼───────┐
│ AI Detector  │────────▶│        Go Backend (API + logic)          │
│ YOLOv8 +     │         └────┬──────────┬───────────┬──────────────┘
│ YAMNet +     │              │          │           │
│ InsightFace  │        ┌─────▼───┐ ┌────▼────┐ ┌────▼──────┐
└──────────────┘        │PostgreSQL│ │ MinIO   │ │   ACS     │
                        │ metadata │ │ archive │ │Hikvision/ │
                        └──────────┘ └─────────┘ │Dahua/     │
                                                  │Promwad    │
                                                  └───────────┘
```

---

## Interface languages

The interface switches between four languages: **Russian, English, Chinese
(simplified) and Korean**. The default is Russian.

| Language | Button on the "Server" page | Translation state |
|---|---|---|
| Русский | default | source language, complete |
| English | `English` | partial |
| 简体中文 | `简体中文` | partial |
| 한국어 (DPRK) | `한국어` | partial, **South Korean norm** — see below |

### If the language was not picked up automatically

On first open the interface takes the language from the browser settings: if the
browser reports Chinese or Korean, the interface opens in it right away. But it
can go the other way — the browser is set to Russian while the person needs
another language; or the browser reports a language the interface does not have,
and Russian is shown. Then the language is changed by hand.

![Language switcher](docs/screenshots/30-language-switch.png)

1. Open the **Server** page — `http://<server address>:3001/server`, the same
   address you use to open the interface.
2. In the **Interface language** block, press the button of the language you
   need. The block comes first on the page, right under the intro text: a
   person who does not read Russian would never get to the other settings
   without understanding what is written there.
3. The chosen language is ticked and applied at once — to the menu, the
   buttons and the labels on every page.

The choice is remembered in the browser, so it only has to be set once. It is
stored in the browser, not on the server: one workstation can be in Russian
while another is in Korean, and they will not interfere with each other.

More screenshots of the same screen: [in English](docs/screenshots/27-language-en.png),
[in Chinese](docs/screenshots/28-language-zh.png),
[in Korean](docs/screenshots/29-language-ko.png),
[the whole page](docs/screenshots/26-language-ru.png).

### Why the flags are drawn in code

The flags are drawn as vectors right in the code — they are not emoji and are
not loaded as images. The reason is the same as with the dictionaries: the
server stands in a house or an office where there may be no internet at all,
and any outside download in such a network turns into an empty space instead
of a flag. This has already happened with fonts: without a font with CJK
glyphs the whole Chinese text showed up as empty rectangles.

### About the Korean language

The button carries the **DPRK** flag, while the text is translated using the
**South Korean norm**. This divergence is deliberate: the customer for this
server is in the DPRK, and the flag should be that of their country. But the
North Korean norm is not "the same Korean with a different flag" — it is a
separate translation language with its own vocabulary, forms of address and
terminology, and it requires a native speaker. It cannot be done by swapping
words in a dictionary.

How to help with the translation, how to fix any other language and how to add
a new one — [docs/TRANSLATIONS.md](docs/TRANSLATIONS.md).

---

## Features

| Area | What is implemented |
|---|---|
| **Cameras** | Adding and editing through the web interface (data in PostgreSQL, no hand-written configs), subnet scanner (ONVIF, mDNS, HTTP probing, credential brute-force), **stream check before saving** showing codec, resolution and presence of sound |
| **Streams** | RTSP ingest through go2rtc, MSE and WebRTC for the browser, WebRTC, main and sub stream, proxying through the backend with JWT authorisation |
| **External RTSP access** | Publishing streams to third-party systems (video walls, recorders, analytics) at `/cameras/{N}/streaming/{main\|sub}` on a separate port 9784. Channel numbers are assigned automatically, the **External access** page shows ready-made links. Streams are served without transcoding — the load on the cameras does not grow |
| **Camera audio** | Listening in the player: sound travels inside the main stream, go2rtc transcodes G.711 to Opus only for whoever listens. Two-way audio (browser microphone → camera speaker) on ONVIF Profile T cameras |
| **Object detection** | YOLOv8 with tracking. Accuracy filters: confidence threshold, minimum and maximum object size, bounding box aspect ratio, suppression of motionless objects. Configured per camera — [docs/DETECTION-QUALITY.md](docs/DETECTION-QUALITY.md) |
| **Face recognition** | InsightFace (ArcFace): embeddings, a directory of known faces, marks "known" / "unknown" / "banned" |
| **Licence plate recognition** | Plate area detection, OCR, a configurable search zone (so as not to capture the camera's OSD menu), region-aware format validation |
| **PTZ** | Pan-tilt-zoom cameras over ONVIF: direction, zoom, stop, presets, step adjustment |
| **Camera control** | Camera settings through the firmware HTTP API (Majestic) without SSH: video, image, night mode, OSD, restart. Cameras without an API still use SSH |
| **Cameras of other makes** | Device passport straight from the camera (model, firmware, serial number, MAC), state (uptime, CPU and memory load, **clock drift**), stream parameters and **reboot** — for Hikvision (ISAPI), Beward (vendor HTTP API) and through the common ONVIF layer: Vivotek, Dahua, Axis, Uniview, Reolink, Xiongmai. Works without SSH — [docs/CAMERA-VENDORS.md](docs/CAMERA-VENDORS.md) |
| **Camera health** | Polling `/metrics` and `/api/v1/sources` once a minute: CPU load, free memory, sensor fps, encoder stalls. A verdict of "normal / watch / problem" with the reasons spelled out |
| **Previews** | A frame from the camera instead of a video stream in the camera list: one HTTP request instead of an RTSP session. Basic and Digest authentication supported, fallback to RTSP |
| **Events** | Storage of AI detections (class, confidence, bbox, track), binding to a trigger (object / line / face / plate), filters and pagination |
| **Archive** | Recording by trigger or continuously, metadata in PostgreSQL, files locally or in MinIO, playback in the browser, automatic cleanup by retention period |
| **Access control** | Adapters for Hikvision (ISAPI), Dahua, Promwad, **IronLogic Z-5R WEB BT**: list of doors, opening, access events |
| **Access: people and rights** | Card holders (name, job title, photo, department), access groups, doors, rights calculation with the source shown. Key types: simple, master, blocking. A separate "Access" page — [docs/SKUD-ACCESS.md](docs/SKUD-ACCESS.md) |
| **Card enrolment** | Accept mode on the controller (the door opens for everyone, cards are written to memory) and **reading a card from the door reader** — the number lands in the interface without opening the door for everyone. A desktop USB reader at the operator's desk works like a keyboard |
| **Floor plans** | Floor layouts with equipment placed on them: cameras, doors, controllers, readers on a background (a photo or a scan of the plan). Every point shows whether the device is working — [docs/PLANS.md](docs/PLANS.md) |
| **PoE switches** | Keeping track of managed PoE switches: discovery on the network, port state (link, speed, consumption in watts), **rebooting a hung camera by power** without walking to it. Protection against switching off uplink ports. Binding cameras to ports — [docs/SWITCHES.md](docs/SWITCHES.md) |
| **Server settings** | A single page: storage paths for recordings and snapshots, local disk or S3, recognition parameters |
| **Monitoring** | Automatic detection of camera status (online/offline), restoring paths after a media server restart, cleanup of "dangling" paths |
| **Notifications** | Telegram and MAX: detection events with a snapshot or a clip, choice of cameras and event types, confidence threshold, quiet hours, delivery log. Separately — server watching: cameras going away, CPU load, memory, disk, overheating and the graphics card |
| **Snapshots** | A frame from the camera along vendor-specific paths, fallback frame from go2rtc, event snapshots in the feed |
| **Mobile app** | Android for phones: server selection, viewing cameras and the archive. A separate build for **Android TV**: grids from 1 to 25 cameras, layouts, sound. Details — [below](#mobile-apps) |

---

## Interface

### Cameras and viewing

The tiles show a live frame from every camera, its status and stream addresses.
Switching `main`/`sub` right in the tile, and a separate delete button.

![Cameras](docs/screenshots/03-cameras.png)

The camera card has a player with the main or the sub stream, a sound button and
a PTZ pad for pan-tilt-zoom models.

![Camera card](docs/screenshots/04-camera-live.png)

Next to the player are details about the device: its passport (model, firmware,
serial number, MAC), state (uptime, CPU and memory load, clock drift), stream
parameters and a reboot. The data is read from the camera itself, not from our
database.

![Device block](docs/screenshots/21-camera-device.png)

### Audio

The camera delivers sound in G.711, which browsers do not play directly.
go2rtc transcodes it to Opus on the fly — only for whoever listens, and inside
the main stream, so no separate audio track is needed. The panel shows
the camera codec, whether transcoding is running and whether sound events are
being recognised.

![Audio settings](docs/screenshots/05-camera-audio.png)

Sound events from YAMNet: gunshot, scream, broken glass, dog barking and
others. Filters by class and highlighting of alarming events.

![Sound events](docs/screenshots/08-audio-events.png)

### Object detection

Object classes, minimum confidence, the search zone and the crossing line. The
settings apply to a specific camera and are picked up by the detector
automatically, without a restart.

![Detection settings](docs/screenshots/06-camera-detection.png)

Found objects are written to the journal with a snapshot, the class, the
confidence and the camera they belong to.

![Detection events](docs/screenshots/07-events.png)

### Archive

Recordings with the reason (object, face, plate, line, continuous recording),
size, resolution and retention period. Playback right in the browser.

![Archive](docs/screenshots/09-recordings.png)

### Recognition and access control

Directories of known faces and vehicle plates with the statuses "known",
"unknown" and "banned".

![Recognition](docs/screenshots/10-recognition.png)

The scanner finds cameras on the subnet, and before adding one you can check
the stream: codec, resolution and whether there is sound.

![Scanner](docs/screenshots/12-scanner.png)

### Access: people, groups and keys

Rights in the card holder's page are shown **as the result of a calculation**,
not as stored records: for every door you can see where the access came from —
from a group or from a personal rule. Otherwise, when sorting out "it does not
open for him", there is no way to know what to fix.

Keys come in three types: a simple pass, a master (programs the controller, does
not open the door) and a blocking one (toggles the mode).

A card number can be read in two ways:

* **a desktop USB reader** at the operator's desk — it works like a keyboard,
  so the number is typed straight into the form field;
* **the reader on the door** — the operator presses "Wait for card", holds the
  card to the door, and the number appears in the interface. The door is **not**
  opened for everyone: the server only listens to the controller's events.

Details, including where the vendor documentation diverges from the live
hardware — [docs/SKUD-ACCESS.md](docs/SKUD-ACCESS.md).

### Floor plans

Floor layouts with equipment placed on them: you can see what stands where and
what has lost its connection.

It is a separate page rather than a tab in the device list: a list and a plan
answer different questions. The list answers "which cameras are there", the plan
answers "what stands at this doorway". During a walk-around and when sorting out
an incident it is the second one that is needed.

![Floor plans](docs/screenshots/18-plans.png)

Cameras, doors, controllers and readers are placed on a background — a photo or
a scan of the floor plan. The icon is green if the device works, red when the
connection is lost, grey if the device was deleted or if it is a place marker
with nothing bound to it. The shape of the icon distinguishes the kind of
device.

Coordinates are stored as fractions of the background size, so the point can be
replaced with a picture of a different size — the layout will not shift. The
state is refreshed once a minute, and the plan header shows how many devices are
offline. Details — [docs/PLANS.md](docs/PLANS.md).

### PoE switches

Keeping track of managed PoE switches and controlling camera power.

The page answers the question the camera list leaves open: why the device went
away. "Offline" does not explain the cause, and there are three of them — the
camera hung, the line broke, or the power was cut off — and the operator's
actions differ in each case.

![Switches](docs/screenshots/19-switches.png)

Switches are discovered with a broadcast request and added with one click. The
card shows a port table: link and speed, PoE state, consumption in watts, the
bound camera. Every port can be turned on, turned off or power-cycled — the last
one is what replaces walking up to a hung camera.

A hung camera is rebooted straight from the card: the "Connection" block
compares the power, the link and the camera's own state and says what is wrong,
instead of leaving the operator with two numbers side by side.

![Camera connection](docs/screenshots/20-camera-network.png)

Uplink ports are protected from being switched off: the link to the server goes
through them, and cutting their power can cost you control of the whole switch.
Every action is written to the journal with who did it and when. Details,
including the differences between models — [docs/SWITCHES.md](docs/SWITCHES.md).

### Settings and overview

A single settings page: where to store recordings and snapshots (disk or S3),
recognition parameters, retention period.

![Settings](docs/screenshots/11-settings.png)

A summary of the system state: cameras online, events in 24 hours, space used.

![Dashboard](docs/screenshots/02-dashboard.png)

### Notifications

Events go to Telegram and MAX: a snapshot or a video clip, a choice of cameras,
event types, confidence threshold and quiet hours. Each channel is configured
separately, and one failing does not hinder the other.

![Telegram notifications](docs/screenshots/14-notify-telegram.png)

MAX is the only channel reachable from Russia directly, without a proxy. Its
REST API is markedly stricter about the message format, so the text is built
separately from Telegram rather than reused.

![MAX notifications](docs/screenshots/15-notify-max.png)

A separate tab watches the server itself: a camera went away, high CPU load, low
memory, the disk is running out, overheating, a graphics card failure. Measuring
temperature and the graphics card needs an agent on the host: they are not
visible from inside a container.

![Server notifications](docs/screenshots/16-notify-system.png)

The journal shows what went where and why something did not go — for example, an
event type is not selected for sending. From it you can see the delivery state
without opening the messenger.

![Notification journal](docs/screenshots/17-notify-log.png)

> The screenshots were taken on a real system. They can be recreated with
> `node scripts/make-screenshots.js` while the server is running.
> Tokens and chat addresses are wiped from the screenshots automatically.

---

## Mobile apps

One React Native application is built in two forms: for a phone and for a
television. The form is chosen by the device type — on a television the phone
interface would work, but you cannot hit it with a remote.

### Phone

Connecting to the server by IP address, port or DNS name, live viewing of
cameras and working with the archive.

| Screen | What it does |
|---|---|
| **Servers** | A list of saved servers, adding, checking the connection before saving, switching, deletion |
| **Login** | Login and password; the token is stored separately for each server, so switching between them does not require logging in again |
| **Cameras** | Tiles with preview frames (refreshed every 10 seconds), online/offline status, pull-to-refresh |
| **Viewing** | Live video over MSE/WebRTC, switching the main and sub stream, pause, sound |
| **Archive** | A list of recordings with a filter by camera, a built-in player with seeking, loading more on scroll. The reason for the recording is visible at once: plate, face, object |

| Servers | Cameras | Archive: plates | Archive: objects |
|---|---|---|---|
| ![](docs/screenshots/mobile/01-servers.jpg) | ![](docs/screenshots/mobile/02-cameras.jpg) | ![](docs/screenshots/mobile/04-archive-plates.jpg) | ![](docs/screenshots/mobile/05-archive-objects.jpg) |

The counter next to a server name shows how many cameras are online out of the
total: in the screenshot 19 of 22. A camera can be filtered by address, and a
recording opened straight from the list — for example, "unknown: P263AT54".

### Android TV

A build for televisions and set-top boxes: a grid of up to 25 cameras, layouts
and sound. Controlled with a remote, the tiles are large — visible from the
sofa.

![4×4 grid on a TV](docs/screenshots/tv/01-grid-4x4.jpg)

**Grids:** 1, 2, 4, 6, 9, 16 and 25 cameras. The shape is chosen for the
television screen rather than made a square: 6 cameras is 3×2, not 2×3,
otherwise the tiles are stretched vertically and black bars remain at the sides.

**Layouts:** a set of cameras, the grid size, labels and sound are saved under a
name ("Entrance", "Night shift") and switched with one button. Every server has
its own layouts. The order of the chosen cameras determines their place in the
grid, which is why the selection list shows a tile number rather than a tick.

**Sound:** enabled for one camera at a time. Sound from several cameras at once
merges into noise, while the operator needs one specific camera.

> In dense grids (16 and 25 cameras) the tile labels are hidden automatically:
> such a tile gets about a hundred pixels, and a name would cover half the
> picture. A status dot remains in the corner.

Sound travels inside the main stream: go2rtc transcodes G.711 to Opus for
whoever listens, so no separate audio stream is needed. In the grid the sub
stream is played instead of the main one — four 4K main streams would need
more than 60 Mbit/s, which neither the television's Wi-Fi nor its decoder
would survive.

| Screen | What it does |
|---|---|
| **Grid** | Cameras by layout, focus on a tile, opening full screen or turning the sound on with "OK" |
| **Layouts** | Creating, choosing, deleting; each has a grid size and a set of cameras |
| **Cameras** | Choosing cameras for a layout, the order sets the position |
| **Settings** | Labels (name, status, clock), sound, volume, the camera with sound |

### Building the apps

```bash
cd mobile
npm install
echo "sdk.dir=$HOME/Android/Sdk" > android/local.properties

cd android
JAVA_HOME=/usr/lib/jvm/java-17-openjdk-amd64 ./gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

> **JDK 17** is required: on JDK 21 the build fails with
> `jlink executable ... does not exist`.

The APK is built for both ARM architectures: `arm64-v8a` for modern devices and
`armeabi-v7a` for older set-top boxes and televisions. The finished file is about
76 MB. The device architecture can be found like this:

```bash
adb shell getprop ro.product.cpu.abi
```

> If the APK has no matching architecture, the installation fails with
> `INSTALL_FAILED_NO_MATCHING_ABIS`. The values of `abiFilters` in
> `app/build.gradle` and `reactNativeArchitectures` in `gradle.properties` must
> match: the first decides which prebuilt libraries go into the APK, the second
> decides what React Native's own C++ is compiled for.

**Technologies:** React Native 0.75, `react-native-video` (ExoPlayer/Media3),
TypeScript. Video goes over HLS through the server API (ExoPlayer does not
support MSE), the archive as ready MP4 files.

### Connecting

The address is entered in any form, the application normalises it:

| Input | Result |
|---|---|
| `192.168.1.10` | `http://192.168.1.10:8080` |
| `192.168.1.10:9000` | `http://192.168.1.10:9000` |
| `nvr.local` | `http://nvr.local:8080` |
| `https://cam.example.com` | `https://cam.example.com` |

The **Check connection** button calls the server's `/health` — a mistake in the
address or port is visible before saving.

### Differences from the web interface

The applications have no detection, events, access control, PTZ or camera
settings — they are a tool for viewing and watching. Control stays in the web
interface.

Full guide: [docs/MOBILE.md](docs/MOBILE.md) — installing the Android SDK,
diagnosing "Unable to load script", reducing the APK size, running with hot
reload.

---

## Home Assistant

The integration brings cameras, detection events and access control door
commands into Home Assistant. A dashboard card is installed along with it — an
event feed with frames.

![Camera events in Home Assistant](docs/screenshots/24-ha-card-events.png)

| What | How many |
|---|---|
| Cameras | one per camera, the stream goes through the server's RTSP proxy |
| Motion sensors and events | one per camera |
| Camera buttons | start the stream, recreate the stream, reboot |
| Door buttons | one per access control door |
| Server sensors | cameras online, events in 24 hours, access control, disk |

Events arrive **instantly**: the server pushes them with a request to the
assistant's address instead of waiting for the next poll. The request is signed
with a shared secret.

The assistant takes camera streams through the external RTSP proxy (`9784`) —
**the cameras are not loaded by this**, the extra reader appears at the media
server.

### Installation

Through HACS: **HACS → Integrations → Custom repositories**, add
`https://github.com/himik19872/OpenIPC-NRV` with the type **Integration**,
install **OpenIPC NVR** and restart Home Assistant.

Manually: copy the `custom_components/openipc_nvr` directory into the
assistant's configuration directory and restart it.

Setup: **Settings → Devices & Services → Add integration → OpenIPC NVR**. The
server address, an administrator login and password are needed, and for the
stream — the external RTSP access credentials.

Full guide: [docs/HOME-ASSISTANT.md](docs/HOME-ASSISTANT.md) — every entity
explained, automation examples, card settings, viewing the archive and what to
do when it does not work.

---

## Quick start

### Requirements

- Linux (tested on Ubuntu)
- Docker 24+ and Docker Compose v2
- 2 GB of RAM and 10 GB of disk as a minimum
- Cameras on the same network, reachable over RTSP

### Installation

```bash
git clone https://github.com/himik19872/OpenIPC-NRV.git
cd OpenIPC-NRV

# Create the configuration and set the secrets
cp .env.example .env
sed -i "s|^JWT_SECRET=.*|JWT_SECRET=$(openssl rand -hex 32)|" .env

# Start
./scripts/nvr.sh start
```

The script prints the addresses to use. The web interface is at
`http://<server IP>:3001`, login `admin` / `admin123`.

> **Change the administrator password immediately.** The account is created by a
> seed migration with publicly known credentials.

### Adding cameras

1. Open the **Scanner** section and enter the subnet, for example
   `192.168.1.0/24`.
2. Press **Scan** — the cameras found appear as a list.
3. Choose the ones you need and add them: the stream paths and credentials are
   filled in automatically.
4. Or add them manually in the **Cameras** section, specifying `main_stream` and
   `sub_stream`.

For OpenIPC cameras use the credentials set in Majestic (by default in the
project — `root` and the password from the firmware).

---

## Starting on boot

```bash
sudo ./scripts/install-service.sh
```

This creates the systemd service `nvr`, which brings the containers up when the
system starts. The containers additionally have a `restart: unless-stopped`
policy, so they restart after failures automatically.

```bash
systemctl status nvr          # service state
journalctl -u nvr -f          # startup logs
sudo ./scripts/install-service.sh --uninstall   # remove autostart
```

---

## Management

```bash
./scripts/nvr.sh start      # start
./scripts/nvr.sh stop       # stop
./scripts/nvr.sh restart    # restart
./scripts/nvr.sh status     # state and addresses
./scripts/nvr.sh logs       # logs
./scripts/nvr.sh update     # rebuild the images and update
./scripts/nvr.sh backup     # dump the database to ./backups
```

---

## Documentation

| Document | Contents |
|---|---|
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | Detailed deployment, configuration, TLS, remote access |
| [docs/EXTERNAL-RTSP.md](docs/EXTERNAL-RTSP.md) | Publishing streams to third-party systems: addresses, credentials, **how the OSD is output** |
| [docs/API.md](docs/API.md) | Full description of the REST API with examples |
| [docs/DETECTION-QUALITY.md](docs/DETECTION-QUALITY.md) | Tuning detection accuracy: how to remove false positives and get an object back into a recording |
| [docs/PLANS.md](docs/PLANS.md) | Floor plans: layouts, equipment placement, device state |
| [docs/SWITCHES.md](docs/SWITCHES.md) | PoE switches: discovery, port monitoring, rebooting cameras by power, differences between models |
| [docs/CAMERA-VENDORS.md](docs/CAMERA-VENDORS.md) | Cameras of other manufacturers: the common ONVIF layer, the Hikvision adapter, authorisation, verified capabilities |
| [docs/SKUD-ACCESS.md](docs/SKUD-ACCESS.md) | Access: card holders, groups, doors, key types, working with controllers |
| [docs/MOBILE.md](docs/MOBILE.md) | The Android app: features, building, installing, error diagnosis |
| [docs/TRANSLATIONS.md](docs/TRANSLATIONS.md) | Interface translations: how to fix one, how to add a language, the state of Korean |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Development plan: CUDA detection, Home Assistant |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | How the system is put together, the database schema, decisions taken |
| [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md) | Diagnosing typical problems |
| [plans/](plans/) | The original project plan |

An interactive list of endpoints is available on a running server:
`http://<server IP>:8080/api/v1/docs`

---

## Architecture

| Component | Technology | Purpose |
|---|---|---|
| Backend | Go 1.22, chi, pgx | REST API, business logic, camera control |
| Web interface | React 18, TypeScript, Vite | SPA with an MSE/WebRTC player, a PTZ pad and settings |
| Media server | go2rtc | RTSP ingest, MSE/WebRTC delivery, recording |
| Mobile app | React Native, ExoPlayer | Viewing cameras and the archive from an Android phone and TV |
| Notifications | Telegram Bot API, MAX Bot API | Messages about detection events and server state |
| AI detector | Python, YOLOv8, YAMNet, InsightFace | Objects, sounds, faces, plates (GPU CUDA) |
| Event bus | NATS JetStream | Frames from cameras to the detector, events into the database |
| Database | PostgreSQL 16 | Cameras, events, archive, sound, face and plate directories |
| Storage | MinIO (S3) or a local disk | Video archive and snapshots |

### Repository structure

```
backend/            Go backend
  cmd/server/       entry point
  internal/
    api/            router and HTTP handlers
    service/        business logic, PTZ, SSH, scanner, sound, recognition
    repository/     PostgreSQL and MinIO
    media/          integration with the media server
    monitor/        watching cameras and hardware, thresholds and reminders
    notify/         sending to Telegram and MAX, building messages, the journal
    nats/           subscriptions to detection and sound events
    sysinfo/        polling CPU, memory, disk (Linux)
    tunnel/         WireGuard management
  migrations/       SQL migrations (001-007)
webui/              React SPA
mobile/             React Native apps (phone and TV in one build)
  src/net/          API client and server address parsing
  src/screens/      phone screens: servers, login, cameras, viewing, archive
  src/tv/           television mode: grid, layouts, settings
  src/storage/      storing layouts and servers on the device
  src/state/        the selected server, token, client
  android/          the native part: building the APK, detecting a television
ai-detector/        Python detection service (objects, sound, faces, plates)
scripts/            management and installation scripts
docs/               documentation
plans/              project materials
```

---

## Development

### Backend

```bash
cd backend
go build ./...          # build
go vet ./...            # static analysis
go test ./...           # tests
```

Running locally requires PostgreSQL on `localhost:5434`:

```bash
docker compose up -d postgres minio go2rtc nats
go run ./cmd/server
```

### Web interface

```bash
cd webui
npm install
npm run dev             # dev server on :3000 with a proxy to :8080
npm run build           # production build
```

### AI detector

```bash
cd ai-detector
pip install -r requirements.txt
python frame_publisher.py --rtsp rtsp://localhost:8554/<camera-id> --camera-id <uuid>
python main.py
```

The `yolov8n.pt` model is downloaded automatically on the first run.

---

## Security

Before exposing the server to the internet:

- [ ] Change `JWT_SECRET` (`openssl rand -hex 32`) and `DB_PASSWORD`
- [ ] Change the administrator password in the web interface
- [ ] Change the external RTSP access password (`EXTERNAL_RTSP_PASS`) — the default is `viewer`
- [ ] Close the PostgreSQL (5434), MinIO (9000/9001) and NATS (4222) ports to the outside network
- [ ] Set up TLS termination (nginx/Caddy) in front of the web interface
- [ ] Restrict access to the go2rtc API (1984)
- [ ] Do not use the default camera credentials

More — in [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md).

---

## Known limitations

| Limitation | Cause | Workaround |
|---|---|---|
| Two-way audio through the camera speaker does not work | The cameras do not support a return audio channel: the `RTSP OPTIONS` reply has no `ANNOUNCE` and `RECORD` methods. The pipeline is implemented and will work on cameras with such support | Checked automatically: the talk button appears only when supported |
| Sound detection uses CPU | YAMNet runs on the CPU so as not to compete with the GPU for object detection | Disable unnecessary cameras or sound classes in the camera settings |
| WireGuard tunnels are not managed automatically | `internal/tunnel/wg_manager.go` is a stub, there is no netlink integration | Set up the peers by hand with `wg-quick` |
| Temperature and graphics card state are visible only through the agent | There is no access to the host's sensors and GPU from inside a container | Install `host-agent` on the host: `sudo install -m 0755 agent.py /opt/nvr-agent/agent.py` |
| The Korean interface uses the South Korean norm while the flag is the DPRK one | The North Korean norm is a separate language (`ko-KP`) with its own rules and terminology and needs a native speaker. The flag, though, should be that of the customer's country | A temporary measure. A proofreader for the North Korean norm is needed: [docs/TRANSLATIONS.md](docs/TRANSLATIONS.md) |
| Some error messages stay in Russian | The server builds them from the device's answer rather than from the interface, and they are not labels | Narrows as the migration goes on. List of places: [docs/TRANSLATIONS.md](docs/TRANSLATIONS.md) |
| Chinese and Korean need a font with CJK glyphs | Vectors are always drawn, but CJK characters are text and are drawn by the system font | Install Noto Sans SC / Noto Sans KR on the machine where the interface is open |

---

## What's new

### 2026-10-04 — interface languages: Russian, English, Chinese, Korean

**The interface switches between four languages.** The switcher sits on the
server settings page, next to the time and network settings. The choice is
remembered in the browser, so the language can differ between workstations — the
server stores nothing here.

**The languages are shown as flags, and the pictures for them are vectors in the
code.** There are no outside downloads: the server stands on a network where
there may be no internet at all, and an empty space instead of a flag is a stage
we have already been through, as it was with the font for CJK glyphs.

**Every interface label is translated.** Each screen was checked by walking it
in Chinese and Korean: the only Russian left is camera and controller names,
which people type in. Russian stays the fallback language, so a label that was
missed would show a word rather than a key like `nav.cameras`.

**About Korean, honestly.** The text is translated using the **South Korean
norm** while the switcher carries the **DPRK** flag — at the request of a
customer from there. The North Korean norm is a separate language with its own
words and terminology and needs a native speaker. For now the flag and the norm
do not match, and this is described in [docs/TRANSLATIONS.md](docs/TRANSLATIONS.md)
together with instructions on how to fix the translation and add a new
language.

### 2026-10-03 — Beward intercom in access control: opening a door with logging

**The intercom is registered in access control as an access device.** The
DS07P-LP has a lock relay and a call button, and the access journal, event
snapshots and video recording already exist in the access control section — so
the intercom is described there rather than next to the cameras.

**How it is wired at the site.** The intercom relay is connected to the "exit
button" input of the Z5R controller.

**What this means for the journal — and this is the main reason for such a
scheme.** For the Z5R, opening from the intercom looks like pressing the exit
button, not like an access by card. Such records already exist in the access
control events: `event_type=exit_button`. From them **it is impossible to tell
who opened the door and from where** — from the intercom, from the Z5R web
interface, or with the real button by the door. All three cases are
indistinguishable. That is why we keep our own journal: it is the only way a
trace remains of the door having been opened by the intercom.

**One opening produces two records** — ours with the reason and the
`exit_button` one from the Z5R. That is not two entries, it is one action seen
from two sides.

**The door state is shown as "unknown", and that is honest.** The device reports
only alarms, and on the unit we checked it always answers `NO Alarm`. The door
position cannot be read. Showing "locked" without knowing would be the same
mistake as the camera clock that showed "exact" while the time had not been
read.

**What has been verified on the live device:** the connection (the controller is
`online`) and the list of doors. **Opening the door has not been verified** —
verifying it means opening a real door. The address and the command parameters
were checked against the interface description and are covered by tests on a
stand-in device.

**SIP and sound are already configured at the site** and are not used by us for
now: registration with a SIP server, calls to two numbers, digits during a call
close the relay. Two-way sound from the device side is enabled
(`DoubleAudio=on`) — the microphone and speaker are given to the RTSP stream we
already receive.

Details — [docs/SKUD-ACCESS.md](docs/SKUD-ACCESS.md).

### 2026-10-03 — Beward cameras: the vendor HTTP API

**The manufacturer handed over a complete interface description** — 219 pages,
63 different addresses. It covers the whole configuration of the device, and
that is an order of magnitude more than ONVIF gives.

**For Beward the vendor API was chosen as the main route, ONVIF as the
fallback.** ONVIF works on these devices, but it does not report uptime at all.
The vendor API does, and now the card shows when the device last booted. On top
of that the vendor interface remains available if ONVIF is switched off on the
device.

**Authorisation is Digest, with Basic as a fallback.** The description allows
both; without the fallback a device configured for Basic would have been
unreachable.

**Two bugs were found, and the second is more dangerous than the first.** The
device time was parsed as universal and recalculated into local — the reading
shifted by the time zone offset, and the intercom clock showed `17:24` instead
of `14:24`. The time must be assembled afresh in the local zone, not converted
from a moment.

The second surfaced in a screenshot: when the clock could not be read, the card
showed "01.01.1" — an invented date — and printed the drift as **"exact"**. That
is, missing data was turning into a statement that the camera clock was correct,
and the operator would not go and check something that had been confirmed as
working. Now missing data looks like missing data — the line is simply absent,
just as with the ONVIF bitrate.

**Cross-checking two sources.** There is no separate serial number field in the
reply, but `DeviceID` is one: the same device reports exactly this value over
ONVIF (`293239`). That is a check, not a guess.

**This model has no face recognition.** The section exists in the description,
but `facecfg_cgi` answers `404`: the set of capabilities depends on the model,
not on the version of the description.

**It became clear why ONVIF works on Beward but not on Hikvision.** On Beward it
is enabled from the factory (`root.Properties.API.ONVIF.ONVIF=yes`), while on the
Hikvision cameras it was never enabled and no ONVIF user was created.

Details — [docs/CAMERA-VENDORS.md](docs/CAMERA-VENDORS.md).

### 2026-10-03 — cameras of other manufacturers: a common ONVIF layer and Hikvision

**Passport and state straight from the camera.** A **Device** block has appeared
on the camera card: model, firmware, serial number, MAC, uptime, CPU and memory
load, stream parameters. The data is read from the camera itself, not from our
database — a divergence from what is recorded is itself a reason to pay
attention.

**Reboot without SSH.** A button on the device card with a confirmation. Verified
on live cameras: they come back in 80 seconds (Hikvision) and 60 seconds
(Vivotek).

**Two levels of access.** The common ONVIF — one piece of code for all makes —
and the vendor Hikvision adapter (ISAPI) where the common language gives less.

**Why Hikvision is not through ONVIF.** On the cameras at the site ONVIF failed:
the preliminary calls pass, but those requiring authorisation are rejected with
"sender is not authorized". The cause is not in the code — ONVIF is not enabled
on the camera and no ONVIF user has been created; a web user is not one. HTTP
Digest with the same password works at once.

**Clock drift is visible in the interface.** On camera `192.168.1.63` the clock
is behind by **139 days** — this is visible right in the card. Before, the only
way to learn about it was shifted timestamps in the archive.

**Three bugs were found and fixed:**

* The sign of the clock drift was reversed: a positive value meant lagging
  behind, but was shown as "running fast". Verified on a camera 139 days behind.
* The reboot button was shown where it would certainly have returned a refusal:
  the capability check stood after a type assertion which always succeeds on a
  chain of adapters.
* The media service address was taken from the camera as is. For a camera behind
  a tunnel it holds its internal address there — the request went nowhere while
  the device was perfectly healthy. Now only the host is substituted, the port
  and path are preserved.

**Authorisation was moved into a common package** `internal/httpdigest` — with
restoring the request body before the retry. Rebooting a Hikvision requires a
non-empty body, and without restoring it the command would silently do nothing.

Details — [docs/CAMERA-VENDORS.md](docs/CAMERA-VENDORS.md).

### 2026-10-02 — determining camera bindings from the MAC table

**Camera bindings to ports are determined automatically**

A switch keeps a table of MAC addresses and reports through which port each
address is visible. The system matches these addresses against the camera
database and proposes the binding itself — the port no longer has to be
specified by hand.

- The switch page shows all devices on the ports, including those that are not
  among the cameras: this helps to find extra equipment
- The "Detect camera bindings" button finds matches and shows what will change
- Devices behind the uplink port are marked separately: they are not connected
  to this switch, and binding them here would be wrong

The selection rules are deliberately narrow, because the price of a mistake is
high — a wrong binding leads to power-cycling the wrong camera. A proposal is
made only if the address is visible on exactly one PoE port and belongs to
exactly one camera of the project.

**Models behave differently, and this is visible in the interface**

There are three cases: the ports are reported (automatic binding), the table
exists but without ports (binding by hand), the command is not supported. On the
live fleet all three turned up, and on one switch the firmware returns the same
mask for all records, including devices on its own ports.

**Port numbering was fixed**

The reversal rule was taken from the switches' own web interface and confirmed
on two models. The previous version reversed the whole port list, whereas the
device reverses **only the PoE ports** and leaves the uplink ones at the end. On
the GPS204V3 this meant that a command addressed to port 1 landed on the uplink
port — the one the link to the server goes through.

The rule was not guessed: in the GPS204V3 interface the `portIndex` set is
`[3,2,1,0]` with four PoE ports, and on the PS208GV3 it is `[0..7]`.

> **When updating:** the fix changes the meaning of already saved bindings on
> models with the reversed order. Check the bindings created earlier.

**Verified on live devices**

The whole chain: the system detected a camera on port 1, power-cycling that port
switched off exactly that camera, and after power was restored the camera
returned to the table on the same port.

### 2026-10-02 — controlling PoE switches

**Keeping track of switches and monitoring ports**

A separate page: discovering switches on the network, port state, an action
journal. Managed SSCPOE PoE switches are supported — verified on the PS204,
PS208GV3 and GPS204V3.

- Discovery with a broadcast request: the address does not have to be known in
  advance, the serial number serves as the identifier
- A port table: link and speed, PoE state, **consumption in watts**, transmitted
  and received volume, the bound camera
- Polling once a minute in the background, the state is available even without
  the interface open

**Rebooting a hung camera by power**

A camera that has stopped answering is rebooted with a button press — there is no
need to walk up to it. The power cycle was verified on live devices: the port is
de-energised, a pause is held, and the power is applied again.

**Binding cameras to ports**

The camera card shows which switch and port it is connected to. The block
compares the power on the port, the presence of a link and the camera's own
state and gives a verdict:

- there is power and a link but the camera is silent — it hung, a reboot will help
- there is power but no link — the line is broken, a reboot will not help
- there is no power — the camera is de-energised

**Protection against dangerous actions**

- Uplink ports cannot be switched off: the link to the server goes through them,
  and cutting their power can cost you control of the whole switch
- Ports without PoE support do not accept power commands
- A binding to a non-existent port is rejected
- Power actions require confirmation

**Differences between models, established on live hardware**

One product line behaves differently, and this is taken into account:

- some models report their state without authorisation, some require a password;
  a login requirement is a separate state, not "no link"
- the temperature lies in different fields, and on one of the models the
  "temperature" field carries an unrelated number
- the number of ports and the number of PoE ports differ: on the GPS204V3 there
  are six ports but power on only four
- the port numbering order is reversed on some models; the flag is kept in the
  settings and can be corrected by hand, because a mistake here means a command
  to the wrong port

**Camera passwords removed from screenshots**

It was discovered that the screenshots contained stream addresses of the form
`rtsp://admin:password@…` — that is, camera credentials in the open in public
documentation. The screenshot script now wipes the login:password pair in stream
addresses for **every** frame, not at the author's discretion.

If you have published this project, change the camera passwords: older versions
of the screenshots remain in the git history.

### 2026-09-26 — notifications, phone and Android TV apps

**Telegram and MAX notifications**

Events go to the messengers with a snapshot or a video clip. Verified in real
use: delivery, attachments, the delivery journal.

- Detection events: objects, plates, faces, line crossing, access control,
  sounds
- A choice of cameras and event types for each channel separately
- Confidence threshold, a pause between repeats, quiet hours
- A daily summary at a set time
- A delivery journal: what went, what did not and why
- One channel failing does not block the other: if Telegram is unavailable, MAX
  keeps working
- **MAX** — a second channel for Russia: reachable directly, without a proxy.
  A separate message text is built for it: the MAX API is stricter about markup
  and does not accept Telegram messages as they are

**Watching the server and the cameras**

A separate tab answers the question "what counts as a problem" rather than
"where to send it": the messages go to the same channels.

- A camera disappeared from the network — with a delay so as not to alarm during
  a reboot
- A camera is unreachable at server start — reported at once
- A camera is back online — you can see the problem is gone
- High CPU load, low RAM, the disk is running out
- Hardware overheating and graphics card problems
- Repeated reminders until the problem is resolved, and quiet hours
- Temperature and the graphics card are measured by an agent on the host: they
  are not visible from inside a container

**Notification time**

The time in the messages was in UTC: the host machine's time zone was not passed
into the images. Now `/etc/localtime` is mounted into all containers, and
PostgreSQL additionally gets `TZ` and `PGTZ` — it takes the time zone from the
data directory, and one mounted zone is not enough for it.

**Phone app**

- Viewing cameras with preview frames and online/offline status
- An archive with the reason for the recording: plate, face, object — visible
  from the list
- Sound from the cameras
- A separate token for each server: switching servers does not require logging
  in again

**Android TV app**

- Grids from 1 to 25 cameras, the shape chosen for the television screen
- Named layouts: the set of cameras, the grid size, labels, sound
- Sound for one camera at a time
- Remote control, elements large enough for the distance to the screen
- In dense grids the labels hide themselves: in a small tile they cover the
  picture

---

### 2026-09-22 — external RTSP access, camera control without SSH, frame previews

**External RTSP access**

Publishing streams to third-party systems: video walls, recorders, third-party
analytics. External consumers take the stream from the NVR and do not connect to
the cameras directly — this relieves devices that limit the number of
simultaneous RTSP sessions.

- Addresses `/cameras/{N}/streaming/{main|sub}` on a separate port `9784`
  (the channel number is offset by minus one: channel 1 → `cameras/0`)
- Separate authorisation: external systems get read-only access, internal
  consumers work as before
- Streams **without transcoding** — the CPU load does not grow
- The **External access** page: connection parameters, ready-made links with
  copying, channel state
- Automatic channel number assignment when a camera is added
- Details — in [docs/EXTERNAL-RTSP.md](docs/EXTERNAL-RTSP.md)

**Camera control without SSH**

- Camera settings through the firmware HTTP API (Majestic): video (fps,
  bitrate, codec, frame size), image (brightness, contrast, saturation, mirror),
  night mode, OSD
- Only the changed fields are sent — the rest of the camera settings are left
  alone
- Values are validated before writing: the camera is weak, and a wrong bitrate
  brings the stream down
- Rebooting the camera through the API instead of SSH
- Support for two firmware builds with different sets of endpoints
- Cameras without an API still use SSH

**Camera health**

- Polling `/metrics` and `/api/v1/sources` once a minute
- Metrics: CPU load, free memory, sensor fps, encoder frame drops, night mode
- A verdict of "normal / watch / problem" with the reasons spelled out
- The metrics are visible in the camera list and on the camera card

**Frame previews instead of a stream**

- In the camera list a frame instead of an HLS stream: one HTTP request instead
  of an RTSP session, which is markedly cheaper for a fleet of cameras
- Downscaling the frame on the server: 4K → 480 pixels (470 KB → 17 KB)
- Basic and Digest authentication supported, vendor-specific frame paths
- Fallback extraction of a frame from the video stream when there is no HTTP
  endpoint
- A request queue: no more than three cameras are polled at the same time

**Other**

- Showing OSD labels in the stream is documented: the label is embedded by the
  camera and is the same for the archive, the browser and external systems
- Temporary debug logs of stream recovery were removed

---

### 2026-09-19 — sound, recognition, camera control

**Camera audio**
- Listening to sound in the player: a separate audio track synchronised with the video
- Transcoding G.711 → AAC: browsers do not play G.711 in HLS
- Routing the sound into a separate go2rtc stream, the video is left untouched

**Sound detection**
- YAMNet (521 AudioSet classes): gunshot, scream, shriek, broken glass, explosion, barking, siren, car alarm, speech, music
- Configuring the threshold, classes and cooldown per camera
- Cutting out silence by loudness — protection against false positives on microphone noise
- The **Sounds** section in the interface: filters by class, alarming events highlighted

**Face and plate recognition**
- Directories of known faces and plates, marks "known" / "unknown" / "banned"
- A configurable plate search zone — the OCR no longer captures the camera's OSD menu
- Plate format validation taking the region into account

**Archive and recording**
- Recording by trigger (object, line, face, plate) or continuously
- The trigger named in the file name — it is clear why the recording started
- Playback in the browser, a choice of storage (local disk or S3)

**Camera control**
- Full control through the web interface: data in PostgreSQL, no hand-written configs
- Stream check before saving: codec, resolution, presence of sound
- Filling in the IP from the RTSP address automatically
- Editing the IP and password automatically updates the media server paths
- Buttons to restart the camera and its streamer
- Cameras without a microphone and speaker are detected automatically

**GPU acceleration**
- Detection on CUDA (NVIDIA P104-100): ~13 ms per frame versus 83 ms on CPU

---

## Licence

The project is distributed "as is" for video surveillance tasks.
Third-party components keep their own licences (go2rtc — MIT,
YOLOv8 — AGPL-3.0, MinIO — AGPL-3.0, PostgreSQL — PostgreSQL License).
