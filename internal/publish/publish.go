// Package publish validates and compiles mutable drafts into immutable snapshots.
package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/agentforge/ontology/internal/ontology"
)

type Snapshot struct {
	PublicationID string                         `json:"publicationId"`
	Revision      uint64                         `json:"revision"`
	Hash          string                         `json:"hash"`
	PublishedAt   time.Time                      `json:"publishedAt"`
	Workspace     ontology.Workspace             `json:"workspace"`
	Objects       map[string]*RuntimeObject      `json:"objects"`
	DataSources   map[string]ontology.DataSource `json:"dataSources"`
}

type RuntimeObject struct {
	Key        string                       `json:"key"`
	Vendor     string                       `json:"vendor"`
	Domain     string                       `json:"domain"`
	Definition ontology.ObjectType          `json:"definition"`
	Properties map[string]ontology.Property `json:"properties"`
	Links      map[string]ontology.Link     `json:"links"`
}

func Compile(d ontology.Draft, revision uint64, now time.Time) (*Snapshot, ontology.ValidationErrors) {
	if errs := ontology.Validate(d); len(errs) != 0 {
		return nil, errs
	}
	canonical, _ := json.Marshal(d)
	var compiled ontology.Draft
	if err := json.Unmarshal(canonical, &compiled); err != nil {
		panic("ontology: canonical draft could not be decoded: " + err.Error())
	}
	d = compiled
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	s := &Snapshot{PublicationID: d.Workspace.ID + "-" + hash[:12], Revision: revision, Hash: hash, PublishedAt: now.UTC(), Workspace: d.Workspace, Objects: map[string]*RuntimeObject{}, DataSources: map[string]ontology.DataSource{}}
	for _, ds := range d.DataSources {
		s.DataSources[ds.ID] = ds
	}
	for _, v := range d.Vendors {
		for _, dm := range v.Domains {
			for _, o := range dm.Objects {
				key := v.ID + "." + dm.ID + "." + o.Name
				ro := &RuntimeObject{Key: key, Vendor: v.ID, Domain: dm.ID, Definition: o, Properties: map[string]ontology.Property{}, Links: map[string]ontology.Link{}}
				for _, p := range o.Properties {
					ro.Properties[p.Name] = p
				}
				for _, l := range o.Links {
					ro.Links[l.Name] = l
				}
				s.Objects[key] = ro
			}
		}
	}
	return s, nil
}
