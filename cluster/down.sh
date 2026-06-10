#!/bin/bash
#
# Stop the kubevirtci cluster

set -e

source ./cluster/kubevirtci.sh

$(kubevirtci::path)/cluster-up/down.sh
