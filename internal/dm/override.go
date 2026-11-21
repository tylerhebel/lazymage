package dm

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// OverrideFile is docker-magento's place for local compose changes: it is
// loaded last by bin/docker-compose and bin/update never touches it.
const OverrideFile = "compose.override.yaml"

// Override edits compose.override.yaml while keeping everything else in it
// (other services, comments, order).
type Override struct {
	path string
	doc  *yaml.Node
	orig []byte
}

// LoadOverride reads the project's override file (an empty one if missing).
func LoadOverride(root string) (*Override, error) {
	o := &Override{path: filepath.Join(root, OverrideFile)}
	b, err := os.ReadFile(o.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	o.orig = b
	var doc yaml.Node
	if len(bytes.TrimSpace(b)) > 0 {
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("%s is not valid YAML: %w", OverrideFile, err)
		}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: the top level must be a mapping", OverrideFile)
	}
	o.doc = &doc
	return o, nil
}

// Original is the file's content before any edit (nil if it did not exist).
func (o *Override) Original() []byte { return o.orig }

func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapEnsure(m *yaml.Node, key string, kind yaml.Kind) *yaml.Node {
	if v := mapGet(m, key); v != nil {
		if v.Kind != kind && v.Tag == "!!null" {
			v.Kind, v.Tag, v.Value = kind, "", ""
		}
		return v
	}
	v := &yaml.Node{Kind: kind}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
	return v
}

func (o *Override) service(name string) (*yaml.Node, error) {
	services := mapEnsure(o.doc.Content[0], "services", yaml.MappingNode)
	if services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: services must be a mapping", OverrideFile)
	}
	svc := mapEnsure(services, name, yaml.MappingNode)
	if svc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: services.%s must be a mapping", OverrideFile, name)
	}
	return svc, nil
}

// Image is the image the override sets for a service ("" if none).
func (o *Override) Image(service string) string {
	services := mapGet(o.doc.Content[0], "services")
	if services == nil {
		return ""
	}
	svc := mapGet(services, service)
	if svc == nil || svc.Kind != yaml.MappingNode {
		return ""
	}
	if img := mapGet(svc, "image"); img != nil {
		return img.Value
	}
	return ""
}

// SetImage sets services.<service>.image.
func (o *Override) SetImage(service, image string) error {
	svc, err := o.service(service)
	if err != nil {
		return err
	}
	if img := mapGet(svc, "image"); img != nil {
		img.Kind, img.Tag, img.Value = yaml.ScalarNode, "", image
		return nil
	}
	svc.Content = append(svc.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "image"}, &yaml.Node{Kind: yaml.ScalarNode, Value: image})
	return nil
}

// HasVolume reports whether services.<service>.volumes has an entry.
func (o *Override) HasVolume(service, volume string) bool {
	services := mapGet(o.doc.Content[0], "services")
	if services == nil {
		return false
	}
	svc := mapGet(services, service)
	if svc == nil {
		return false
	}
	vols := mapGet(svc, "volumes")
	if vols == nil {
		return false
	}
	for _, v := range vols.Content {
		if v.Value == volume {
			return true
		}
	}
	return false
}

// AddVolume appends to services.<service>.volumes unless it is there.
// Compose merges volume lists by container path, so the base file's
// volumes are kept.
func (o *Override) AddVolume(service, volume string) error {
	if o.HasVolume(service, volume) {
		return nil
	}
	svc, err := o.service(service)
	if err != nil {
		return err
	}
	vols := mapEnsure(svc, "volumes", yaml.SequenceNode)
	if vols.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s: services.%s.volumes must be a list", OverrideFile, service)
	}
	vols.Content = append(vols.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: volume})
	return nil
}

// Bytes renders the edited file.
func (o *Override) Bytes() ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(o.doc); err != nil {
		return nil, err
	}
	enc.Close()
	out := b.Bytes()
	if len(o.orig) == 0 {
		out = append([]byte("# Local docker-magento changes; loaded last by bin/docker-compose and kept by bin/update.\n# Managed in part by lazymage.\n"), out...)
	}
	return out, nil
}

// Save writes the file atomically.
func (o *Override) Save() error {
	b, err := o.Bytes()
	if err != nil {
		return err
	}
	return WriteFileAtomic(o.path, b, 0o644)
}

// RestoreOverride puts back content returned by Original (nil removes the file).
func RestoreOverride(root string, orig []byte) error {
	p := filepath.Join(root, OverrideFile)
	if orig == nil {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return WriteFileAtomic(p, orig, 0o644)
}
