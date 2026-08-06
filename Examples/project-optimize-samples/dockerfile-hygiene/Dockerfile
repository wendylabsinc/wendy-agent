# Intentionally un-optimized. See ../README.md.
# - FROM node:latest (unpinned tag)
# - apt-get update and apt-get install split across RUN layers, missing --no-install-recommends
# - pip install without --no-cache-dir
# - npm install instead of npm ci despite a lockfile
# - ADD used for a local file that COPY would handle just as well
# - COPY --from drags in the entire builder stage
# - shell-form CMD
FROM node:latest AS builder

RUN apt-get update
RUN apt-get install -y python3-pip
RUN pip install -r requirements.txt

COPY package.json package-lock.json ./
RUN npm install

ADD app.js /app/app.js

FROM node:latest
COPY --from=builder / /
CMD npm start
