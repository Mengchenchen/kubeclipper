/*
 *
 *  * Copyright 2021 KubeClipper Authors.
 *  *
 *  * Licensed under the Apache License, Version 2.0 (the "License");
 *  * you may not use this file except in compliance with the License.
 *  * You may obtain a copy of the License at
 *  *
 *  *     http://www.apache.org/licenses/LICENSE-2.0
 *  *
 *  * Unless required by applicable law or agreed to in writing, software
 *  * distributed under the License is distributed on an "AS IS" BASIS,
 *  * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  * See the License for the specific language governing permissions and
 *  * limitations under the License.
 *
 */

package main

import (
	"fmt"
	"os"

	"github.com/kubeclipper/kubeclipper/cmd/kubeclipper-agent/app"
	_ "github.com/kubeclipper/kubeclipper/pkg/component/nfs"
	_ "github.com/kubeclipper/kubeclipper/pkg/component/nfscsi"
	_ "github.com/kubeclipper/kubeclipper/pkg/scheme/core/v1/cri"
	_ "github.com/kubeclipper/kubeclipper/pkg/scheme/core/v1/k8s"
)

func main() {
	cmds := app.NewKiAgentCommand(os.Stdin, os.Stdout, os.Stderr)
	if err := cmds.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
