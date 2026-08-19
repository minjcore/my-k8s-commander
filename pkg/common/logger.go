package common

import "log"

const LogPrefix = "[Supervisor]"

// Log ghi log theo format: [Supervisor] -> [ModuleName]: Message
func Log(moduleName, message string) {
	log.Printf("%s -> [%s]: %s\n", LogPrefix, moduleName, message)
}
