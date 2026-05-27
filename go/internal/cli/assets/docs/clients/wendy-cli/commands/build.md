Builds your WendyOS app. Leverages `wendy.json` for metadata. If `wendy.json` is missing, it prompts you to generate one.

- Cross-compiles Swift projects and builds a Docker image for projects with a `Package.swift`
- Uses `docker build` for Dockerfile

If multiple targets are available (both Swift and Dockerfile), you get a menu to select which one to use.

The build command is mainly used to verify your app can build/compile.

## Platform support for Swift projects

`wendy build` for Swift packages requires a host Swift toolchain and is supported on **macOS and Linux hosts only**. On Windows, `wendy build` returns an error for Swift projects. The recommended alternative is to provide a `Dockerfile` and use `wendy run`, which routes through the Docker buildx path on all platforms.
