---
title: Getting started
tags: [guide]
---

Getting Started
===============

This guide walks you through installing the tool, configuring it for your
project and running your first build. It assumes you are comfortable with a
terminal and have Go installed. If you are new to Go, read the official tour
first; the rest of this guide will make more sense afterwards.

Installation
------------

Install the command with `go install example.com/tool@latest`. The binary lands
in `$GOBIN`, which defaults to `$HOME/go/bin`. Make sure that directory is on
your `PATH`, otherwise your shell will not find the command.

- On macOS, add the directory in `~/.zshrc`.
- On Linux, add it in `~/.profile` or your shell's equivalent.
- On Windows, edit the user environment variables.

Configuration
-------------

Create `tool.yaml` in the project root:

    name: demo
    targets:
      - linux/amd64
      - darwin/arm64

<!-- The schema is documented separately.
# Not a heading
-->

Options can also come from the environment. Variables override the file, and
flags override both. This lets CI change a single setting without editing the
checked-in configuration.

| Option | Default | Description |
|--------|---------|-------------|
| name | directory name | Project name used in artefacts |
| targets | host platform | Platforms to build for |
| parallel | number of CPUs | Concurrent builds |

## Running a build

Run `tool build`. The first build downloads dependencies and may take a
minute; later builds reuse the cache. Pass `-v` to see each step. When a build
fails, the tool prints the failing step and exits with a non-zero status, so
scripts can stop early.
