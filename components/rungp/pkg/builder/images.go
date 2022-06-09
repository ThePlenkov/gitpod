// Copyright (c) 2022 Gitpod GmbH. All rights reserved.
// Licensed under the GNU Affero General Public License (AGPL).
// See License-AGPL.txt in the project root for license information.

package builder

type GitpodImages struct {
	Supervisor string `json:"supervisor"`
	WebIDE     string `json:"vsCodeWeb"`
}

var DefaultImages = GitpodImages{
	Supervisor: "eu.gcr.io/gitpod-core-dev/build/supervisor:commit-12b58f4979b2a89e9f5e6b33c1bee5f24c0ad079",
	WebIDE:     "eu.gcr.io/gitpod-core-dev/build/ide/code:commit-86282e8b797054f1f8db1f902d3b16b3d121710d",
}