package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"text/template"
)

//go:embed sample.yaml.tmpl
var sampleTemplate string

type Sample struct {
	HubKubeconfig        string
	HubPassiveKubeconfig string
	C1Kubeconfig         string
	C2Kubeconfig         string
	C1Client1Kubeconfig  string
	C2Client1Kubeconfig  string
}

func NewSample() *Sample {
	return &Sample{
		HubKubeconfig:        "/path/to/hub/kubeconfig",
		HubPassiveKubeconfig: "",
		C1Kubeconfig:         "/path/to/c1/kubeconfig",
		C2Kubeconfig:         "/path/to/c2/kubeconfig",
		C1Client1Kubeconfig:  "",
		C2Client1Kubeconfig:  "",
	}
}

func (s *Sample) Bytes() ([]byte, error) {
	t, err := template.New("config").Parse(sampleTemplate)
	if err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, s); err != nil {
		return nil, fmt.Errorf("executing template: %w", err)
	}

	return buf.Bytes(), nil
}

func CreateSampleConfig(filename string) error {
	data, err := NewSample().Bytes()
	if err != nil {
		return err
	}

	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("creating config file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	return nil
}
