package agentmodel_test

import (
	"os"
	"testing"

	"oro/pkg/testutil/configenv"
)

func TestMain(m *testing.M) {
	os.Exit(configenv.Run(m.Run))
}
