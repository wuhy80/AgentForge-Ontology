package ontology

import (
	"fmt"
	"regexp"
)

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

type ValidationError struct{ Path, Message string }

func (e ValidationError) Error() string { return e.Path + ": " + e.Message }

type ValidationErrors []ValidationError

func (e ValidationErrors) Error() string {
	return fmt.Sprintf("ontology validation failed with %d error(s)", len(e))
}

// Validate checks referential and runtime-safety constraints before publication.
func Validate(d Draft) ValidationErrors {
	var out ValidationErrors
	checkID := func(path, value string) {
		if !identifier.MatchString(value) {
			out = append(out, ValidationError{path, "must match " + identifier.String()})
		}
	}
	checkID("workspace.id", d.Workspace.ID)
	sources := map[string]bool{}
	for i, s := range d.DataSources {
		checkID(fmt.Sprintf("dataSources[%d].id", i), s.ID)
		if sources[s.ID] {
			out = append(out, ValidationError{fmt.Sprintf("dataSources[%d].id", i), "duplicate data source"})
		}
		sources[s.ID] = true
		if s.Driver == "" {
			out = append(out, ValidationError{fmt.Sprintf("dataSources[%d].driver", i), "is required"})
		}
	}
	objects := map[string]bool{}
	for _, v := range d.Vendors {
		for _, dm := range v.Domains {
			for _, o := range dm.Objects {
				objects[v.ID+"."+dm.ID+"."+o.Name] = true
			}
		}
	}
	seenObjects := map[string]bool{}
	for vi, v := range d.Vendors {
		vp := fmt.Sprintf("vendors[%d]", vi)
		checkID(vp+".id", v.ID)
		enums := map[string]bool{}
		for ei, e := range v.Enums {
			checkID(fmt.Sprintf("%s.enums[%d].name", vp, ei), e.Name)
			enums[e.Name] = true
		}
		for di, dm := range v.Domains {
			dp := fmt.Sprintf("%s.domains[%d]", vp, di)
			checkID(dp+".id", dm.ID)
			for oi, o := range dm.Objects {
				key := v.ID + "." + dm.ID + "." + o.Name
				op := fmt.Sprintf("%s.objects[%d]", dp, oi)
				checkID(op+".name", o.Name)
				if seenObjects[key] {
					out = append(out, ValidationError{op + ".name", "duplicate object"})
				}
				seenObjects[key] = true
			}
		}
		for di, dm := range v.Domains {
			for oi, o := range dm.Objects {
				op := fmt.Sprintf("%s.domains[%d].objects[%d]", vp, di, oi)
				props := map[string]Property{}
				for pi, p := range o.Properties {
					pp := fmt.Sprintf("%s.properties[%d]", op, pi)
					checkID(pp+".name", p.Name)
					if _, ok := props[p.Name]; ok {
						out = append(out, ValidationError{pp + ".name", "duplicate property"})
					}
					props[p.Name] = p
					if p.Type == TypeEnum && !enums[p.Enum] {
						out = append(out, ValidationError{pp + ".enum", "enum does not exist in vendor"})
					}
					if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
						out = append(out, ValidationError{pp, "min must not exceed max"})
					}
				}
				if o.StorageMode != "" && o.StorageMode != StorageVirtual && o.StorageMode != StorageMaterialized {
					out = append(out, ValidationError{op + ".storageMode", "must be virtual or materialized"})
				}
				if o.Binding == nil {
					out = append(out, ValidationError{op + ".dataBinding", "is required for a runtime object"})
					continue
				}
				if !sources[o.Binding.Source] {
					out = append(out, ValidationError{op + ".dataBinding.source", "data source does not exist"})
				}
				if len(o.Binding.PrimaryKey) == 0 {
					out = append(out, ValidationError{op + ".dataBinding.primaryKey", "at least one key is required"})
				}
				for _, k := range o.Binding.PrimaryKey {
					if _, ok := props[k]; !ok {
						out = append(out, ValidationError{op + ".dataBinding.primaryKey", "property " + k + " does not exist"})
					}
				}
				for name := range o.Binding.Fields {
					if _, ok := props[name]; !ok {
						out = append(out, ValidationError{op + ".dataBinding.fields." + name, "property does not exist"})
					}
				}
				for li, l := range o.Links {
					lp := fmt.Sprintf("%s.links[%d]", op, li)
					checkID(lp+".name", l.Name)
					target := l.Target.Vendor + "." + l.Target.Domain + "." + l.Target.Object
					if !objects[target] {
						out = append(out, ValidationError{lp + ".target", "object " + target + " does not exist"})
					}
					if l.Resolver.Type != "field" {
						out = append(out, ValidationError{lp + ".resolver.type", "MVP supports only field resolver"})
					}
					if _, ok := props[l.Resolver.SourceField]; !ok {
						out = append(out, ValidationError{lp + ".resolver.sourceField", "property does not exist"})
					}
				}
			}
		}
	}
	return out
}
