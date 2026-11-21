// Command agent is the unbranded build of the platform host agent. Products
// ship their own binary by calling agent.Execute with their Options; see
// package agent.
package main

import (
	"os"

	"github.com/bboxsys/platform/agent"
)

func main() {
	if err := agent.Execute(agent.Options{}); err != nil {
		os.Exit(1)
	}
}
