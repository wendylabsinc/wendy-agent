# HelloFileSync

A minimal Wendy file-sync example.

This app keeps the container image small and declares runtime files in
`wendy.json.files` instead of baking them into Docker layers. The checked-in
files are tiny placeholders, but they represent the kind of large assets that
usually cause slow rebuild/push cycles:

- model bundles under `models/`
- prompts under `prompts/`
- runtime config under `config/`

## Why this exists

Container layers are linear. If several large files change independently, one
changed early layer can invalidate unrelated later layers. Cloud-style solutions
usually ask the device to download files from object storage or a model registry,
which requires network access and credentials on the target device.

`wendy.json.files` lets `wendy run` sync those files directly from the developer
machine over the existing Wendy connection. The target device does not need
Wi-Fi, internet, or device-side cloud setup.

## Run it

From this directory:

```sh
wendy run --device <device-name>
```

The image only contains `app.py`. Wendy syncs the files declared in
`wendy.json`, mounts them read-only under the app working directory, and starts
the container.

The app prints the synced files it loaded and serves the current view at:

```text
http://localhost:8000/
```

## Try independent changes

Edit one synced file, then run again:

```sh
printf 'classifier-model-v2\n' > models/classifier/model.txt
wendy run --device <device-name>
```

Wendy can sync the changed classifier bundle without rebuilding the app image or
resyncing unchanged files such as `models/detector` or `prompts/system.txt`.

## File layout

```text
.
├── app.py
├── Dockerfile
├── wendy.json
├── config/runtime.json
├── models/
│   ├── detector/model.txt
│   └── classifier/model.txt
└── prompts/system.txt
```
