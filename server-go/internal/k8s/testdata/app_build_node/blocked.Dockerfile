# syntax=docker/dockerfile:1
# The base's host is pinned to loopback in the build pod by the live test, so this must fail to pull.
FROM blocked.invalid/x:1
