/*
Copyright 2026 The KubeVirt Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"

	"github.com/ghodss/yaml"
	"kubevirt.io/vm-file-restore-operator/pkg/resources/operator"
)

var (
	csvVersion         = flag.String("csv-version", "", "")
	replacesCsvVersion = flag.String("replaces-csv-version", "", "")
	namespace          = flag.String("namespace", "", "")
	pullPolicy         = flag.String("pull-policy", "", "")
	logoBase64         = flag.String("logo-base64", "", "")
	verbosity          = flag.String("verbosity", "1", "")
	operatorVersion    = flag.String("operator-version", "", "")
	operatorImage      = flag.String("operator-image", "", "")
	dumpCRDs           = flag.Bool("dump-crds", false, "Include CRDs in output")
)

//go:embed assets/filerestore.kubevirt.io_virtualmachinefilerestores.yaml
var vmFileRestoresCRD []byte

//go:embed assets/filerestore.kubevirt.io_filerestoreoperators.yaml
var fileRestoreOperatorsCRD []byte

func main() {
	flag.Parse()

	// Validate required flags
	if *csvVersion == "" {
		fmt.Fprintln(os.Stderr, "Error: --csv-version is required")
		flag.Usage()
		os.Exit(1)
	}
	if *namespace == "" {
		fmt.Fprintln(os.Stderr, "Error: --namespace is required")
		flag.Usage()
		os.Exit(1)
	}
	if *operatorImage == "" {
		fmt.Fprintln(os.Stderr, "Error: --operator-image is required")
		flag.Usage()
		os.Exit(1)
	}
	if *operatorVersion == "" {
		fmt.Fprintln(os.Stderr, "Error: --operator-version is required")
		flag.Usage()
		os.Exit(1)
	}

	data := operator.ClusterServiceVersionData{
		CsvVersion:         *csvVersion,
		ReplacesCsvVersion: *replacesCsvVersion,
		Namespace:          *namespace,
		ImagePullPolicy:    *pullPolicy,
		IconBase64:         *logoBase64,
		Verbosity:          *verbosity,
		OperatorVersion:    *operatorVersion,
		OperatorImage:      *operatorImage,
	}

	csv, err := operator.NewClusterServiceVersion(&data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating CSV: %v\n", err)
		os.Exit(1)
	}

	yamlBytes, err := yaml.Marshal(csv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling CSV to YAML: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("---")
	fmt.Print(string(yamlBytes))

	if *dumpCRDs {
		fmt.Println("---")
		fmt.Print(string(vmFileRestoresCRD))
		fmt.Println("---")
		fmt.Print(string(fileRestoreOperatorsCRD))
	}
}
