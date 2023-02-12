#!/usr/bin/env bash

export INSTANCE_ID="${FLY_ALLOC_ID}"

# TODO: can we tell paketo to put the binary in a less suspicious location
/layers/paketo-buildpacks_go-build/targets/bin/wsg
