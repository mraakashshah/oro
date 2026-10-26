package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"time"
)

func factoryServicePlist(intent factoryServiceIntent) ([]byte, error) {
	argv := factoryJobXMLNode("array", "")
	for _, arg := range []string{intent.OroPath, "_supervise", "--state-dir", intent.Root, "--id", intent.ID, "--runtime-config", intent.Runtime.Path, "--runtime-config-sha256", intent.Runtime.SHA256, "--hard-stop", intent.HardStop.UTC().Format(time.RFC3339Nano)} {
		argv.Children = append(argv.Children, factoryJobXMLNode("string", arg))
	}
	keep := factoryJobXMLNode("dict", "")
	keep.Children = []factoryJobXMLValue{factoryJobXMLNode("key", "SuccessfulExit"), factoryJobXMLNode("false", "")}
	properties := []struct {
		key   string
		value factoryJobXMLValue
	}{
		{"Label", factoryJobXMLNode("string", intent.Label)}, {"Program", factoryJobXMLNode("string", intent.OroPath)}, {"ProgramArguments", argv}, {"WorkingDirectory", factoryJobXMLNode("string", intent.Root)}, {"RunAtLoad", factoryJobXMLNode("true", "")}, {"KeepAlive", keep}, {"ThrottleInterval", factoryJobXMLNode("integer", "30")}, {"ExitTimeOut", factoryJobXMLNode("integer", "5")}, {"Umask", factoryJobXMLNode("integer", "63")}, {"StandardInPath", factoryJobXMLNode("string", "/dev/null")}, {"StandardOutPath", factoryJobXMLNode("string", "/dev/null")}, {"StandardErrorPath", factoryJobXMLNode("string", "/dev/null")},
	}
	dict := factoryJobXMLNode("dict", "")
	for _, p := range properties {
		dict.Children = append(dict.Children, factoryJobXMLNode("key", p.key), p.value)
	}
	doc := factoryJobXMLNode("plist", "")
	doc.Version, doc.Children = "1.0", []factoryJobXMLValue{dict}
	data, err := xml.MarshalIndent(doc, "", "\t")
	if err != nil {
		return nil, fmt.Errorf("service plist: %w", err)
	}
	data = bytes.ReplaceAll(data, []byte("<true></true>"), []byte("<true/>"))
	data = bytes.ReplaceAll(data, []byte("<false></false>"), []byte("<false/>"))
	data = append([]byte(xml.Header), data...)
	if len(data) > 64<<10 {
		return nil, fmt.Errorf("service plist exceeds bound")
	}
	return data, nil
}
