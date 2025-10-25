package memforge

import (
	"echo"
	"essence"
	"fmt"
)

const memForgeNamespaceStringRepr = "fe751c12-6ec6-4767-849d-9fe53d56acc4"

var memForgeNamespaceUUID essence.UUID

func init() {
	uuid, err := essence.UUIDFromString(memForgeNamespaceStringRepr)
	if err != nil {
		panic(fmt.Errorf("could not create uuid from string: %s", memForgeNamespaceStringRepr))
	}

	memForgeNamespaceUUID = uuid

	echo.EchoSystemRegister(memForgeNamespaceUUID, echo.EchoSystemConfiguration{
		MinLogLevel:    echo.TRACE,
		SystemPrefixes: []string{"MemForge"},
	})
}
