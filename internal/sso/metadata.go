package sso

import (
	"encoding/xml"
	"errors"

	"github.com/crewjam/saml"
)

// marshalMetadata renders SP metadata as an indented XML document.
func marshalMetadata(md *saml.EntityDescriptor) ([]byte, error) {
	b, err := xml.MarshalIndent(md, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), b...), nil
}

// parseMetadata parses IdP metadata: an EntityDescriptor, or the first entity with an IDPSSODescriptor of an
// EntitiesDescriptor (federation metadata). It replaces samlsp.ParseMetadata, which would pull in a JWT library
// for session cookies openlog does not use.
func parseMetadata(data []byte) (*saml.EntityDescriptor, error) {
	var ed saml.EntityDescriptor
	if err := xml.Unmarshal(data, &ed); err == nil {
		return &ed, nil
	}
	var eds saml.EntitiesDescriptor
	if err := xml.Unmarshal(data, &eds); err != nil {
		return nil, err
	}
	var walk func(e *saml.EntitiesDescriptor) *saml.EntityDescriptor
	walk = func(e *saml.EntitiesDescriptor) *saml.EntityDescriptor {
		for i := range e.EntityDescriptors {
			if len(e.EntityDescriptors[i].IDPSSODescriptors) > 0 {
				return &e.EntityDescriptors[i]
			}
		}
		for i := range e.EntitiesDescriptors {
			if d := walk(&e.EntitiesDescriptors[i]); d != nil {
				return d
			}
		}
		return nil
	}
	if d := walk(&eds); d != nil {
		return d, nil
	}
	return nil, errors.New("no IDPSSODescriptor in the metadata")
}
