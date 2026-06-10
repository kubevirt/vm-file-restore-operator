#!/bin/bash
#
# Start a kubevirtci cluster for development and testing

set -e

source ./cluster/kubevirtci.sh
kubevirtci::install

$(kubevirtci::path)/cluster-up/up.sh
