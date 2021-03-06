package rdf

import (
	"fmt"
)

const (
	JSON_LD_CONTEXT = "@context"
	JSON_LD_TYPE    = "@type"
	JSON_LD_TYPE_AS = "type"
)

// JSONLD is an alias for the generic map of keys to interfaces, presumably
// parsed from a JSON-encoded context definition file.
type JSONLD map[string]interface{}

// ParsingContext contains the results of the parsing as well as scratch space
// required for RDFNodes to be able to statefully apply changes.
type ParsingContext struct {
	Result  *ParsedVocabulary
	Current interface{}
	Name    string
	Stack   []interface{}
	// Applies the node only for the next level of processing.
	//
	// Do not touch, instead use the accessor methods.
	OnlyApplyThisNodeNextLevel RDFNode
	OnlyApplied                bool
	// Applies the node once, for the rest of the data. This skips the
	// recursive parsing, and the node's Apply is given an empty string
	// for a key.
	//
	// Do not touch, instead use the accessor methods.
	OnlyApplyThisNode RDFNode
}

func (p *ParsingContext) SetOnlyApplyThisNode(n RDFNode) {
	p.OnlyApplyThisNode = n
}

func (p *ParsingContext) ResetOnlyApplyThisNode() {
	p.OnlyApplyThisNode = nil
}

func (p *ParsingContext) SetOnlyApplyThisNodeNextLevel(n RDFNode) {
	p.OnlyApplyThisNodeNextLevel = n
	p.OnlyApplied = false
}

func (p *ParsingContext) GetNextNodes(n []RDFNode) (r []RDFNode, clearFn func()) {
	if p.OnlyApplyThisNodeNextLevel == nil {
		return n, func() {}
	} else if p.OnlyApplied {
		return n, func() {}
	} else {
		p.OnlyApplied = true
		return []RDFNode{p.OnlyApplyThisNodeNextLevel}, func() {
			p.OnlyApplied = false
		}
	}
}

func (p *ParsingContext) ResetOnlyAppliedThisNodeNextLevel() {
	p.OnlyApplyThisNodeNextLevel = nil
	p.OnlyApplied = false
}

func (p *ParsingContext) Push() {
	p.Stack = append([]interface{}{p.Current}, p.Stack...)
	p.Current = nil
}

func (p *ParsingContext) Pop() {
	p.Current = p.Stack[0]
	p.Stack = p.Stack[1:]
	if ng, ok := p.Current.(nameGetter); ok {
		p.Name = ng.GetName()
	}
}

func (p *ParsingContext) IsReset() bool {
	return p.Current == nil &&
		p.Name == ""
}

func (p *ParsingContext) Reset() {
	p.Current = nil
	p.Name = ""
}

type NameSetter interface {
	SetName(string)
}

type nameGetter interface {
	GetName() string
}

type URISetter interface {
	SetURI(string) error
}

type NotesSetter interface {
	SetNotes(string)
}

type ExampleAdder interface {
	AddExample(*VocabularyExample)
}

// RDFNode is able to operate on a specific key if it applies towards its
// ontology (determined at creation time). It applies the value in its own
// specific implementation on the context.
type RDFNode interface {
	Enter(key string, ctx *ParsingContext) (bool, error)
	Exit(key string, ctx *ParsingContext) (bool, error)
	Apply(key string, value interface{}, ctx *ParsingContext) (bool, error)
}

// ParseVocabulary parses the specified input as an ActivityStreams context that
// specifies a Core, Extended, or Extension vocabulary.
func ParseVocabulary(registry *RDFRegistry, input JSONLD) (vocabulary *ParsedVocabulary, err error) {
	var nodes []RDFNode
	nodes, err = parseJSONLDContext(registry, input)
	if err != nil {
		return
	}
	vocabulary = &ParsedVocabulary{}
	ctx := &ParsingContext{
		Result: vocabulary,
	}
	// Prepend well-known JSON LD parsing nodes. Order matters, so that the
	// parser can understand things like types so that other nodes do not
	// hijack processing.
	nodes = append(jsonLDNodes(registry), nodes...)
	err = apply(nodes, input, ctx)
	return
}

// apply takes a specification input to populate the ParsingContext, based on
// the capabilities of the RDFNodes created from ontologies.
func apply(nodes []RDFNode, input JSONLD, ctx *ParsingContext) error {
	// Hijacked processing: Process the rest of the data in this single
	// node.
	if ctx.OnlyApplyThisNode != nil {
		if applied, err := ctx.OnlyApplyThisNode.Apply("", input, ctx); !applied {
			return fmt.Errorf("applying requested node failed")
		} else {
			return err
		}
		return nil
	}
	// Special processing: '@type' or 'type' if they are present
	if v, ok := input[JSON_LD_TYPE]; ok {
		if err := doApply(nodes, JSON_LD_TYPE, v, ctx); err != nil {
			return err
		}
	} else if v, ok := input[JSON_LD_TYPE_AS]; ok {
		if err := doApply(nodes, JSON_LD_TYPE_AS, v, ctx); err != nil {
			return err
		}
	}
	// Normal recursive processing
	for k, v := range input {
		// Skip things we have already processed: context and type
		if k == JSON_LD_CONTEXT {
			continue
		} else if k == JSON_LD_TYPE {
			continue
		} else if k == JSON_LD_TYPE_AS {
			continue
		}
		if err := doApply(nodes, k, v, ctx); err != nil {
			return err
		}
	}
	return nil
}

// doApply actually does the application logic for the apply function.
func doApply(nodes []RDFNode,
	k string, v interface{},
	ctx *ParsingContext) error {
	// Hijacked processing: Only use the ParsingContext's node to
	// handle all elements.
	recurNodes := nodes
	enterApplyExitNodes, clearFn := ctx.GetNextNodes(nodes)
	defer clearFn()
	// Normal recursive processing
	if mapValue, ok := v.(map[string]interface{}); ok {
		if err := enterFirstNode(enterApplyExitNodes, k, ctx); err != nil {
			return err
		} else if err = apply(recurNodes, mapValue, ctx); err != nil {
			return err
		} else if err = exitFirstNode(enterApplyExitNodes, k, ctx); err != nil {
			return err
		}
	} else if arrValue, ok := v.([]interface{}); ok {
		for _, val := range arrValue {
			// First, enter for this key
			if err := enterFirstNode(enterApplyExitNodes, k, ctx); err != nil {
				return err
			}
			// Recur or handle the value as necessary.
			if mapValue, ok := val.(map[string]interface{}); ok {
				if err := apply(recurNodes, mapValue, ctx); err != nil {
					return err
				}
			} else if err := applyFirstNode(enterApplyExitNodes, k, val, ctx); err != nil {
				return err
			}
			// Finally, exit for this key
			if err := exitFirstNode(enterApplyExitNodes, k, ctx); err != nil {
				return err
			}
		}
	} else if err := applyFirstNode(enterApplyExitNodes, k, v, ctx); err != nil {
		return err
	}
	return nil
}

// enterFirstNode will Enter the first RDFNode that returns true or an error.
func enterFirstNode(nodes []RDFNode, key string, ctx *ParsingContext) error {
	for _, node := range nodes {
		if applied, err := node.Enter(key, ctx); applied {
			return err
		} else if err != nil {
			return err
		}
	}
	return fmt.Errorf("no RDFNode applicable for entering %q", key)
}

// exitFirstNode will Exit the first RDFNode that returns true or an error.
func exitFirstNode(nodes []RDFNode, key string, ctx *ParsingContext) error {
	for _, node := range nodes {
		if applied, err := node.Exit(key, ctx); applied {
			return err
		} else if err != nil {
			return err
		}
	}
	return fmt.Errorf("no RDFNode applicable for exiting %q", key)
}

// applyFirstNode will Apply the first RDFNode that returns true or an error.
func applyFirstNode(nodes []RDFNode, key string, value interface{}, ctx *ParsingContext) error {
	for _, node := range nodes {
		if applied, err := node.Apply(key, value, ctx); applied {
			return err
		} else if err != nil {
			return err
		}
	}
	return fmt.Errorf("no RDFNode applicable for applying %q with value %v", key, value)
}

// parseJSONLDContext implements a super basic JSON-LD @context parsing
// algorithm in order to build a set of nodes which will be able to parse the
// rest of the document.
func parseJSONLDContext(registry *RDFRegistry, input JSONLD) (nodes []RDFNode, err error) {
	i, ok := input[JSON_LD_CONTEXT]
	if !ok {
		err = fmt.Errorf("no @context in input")
		return
	}
	if inArray, ok := i.([]interface{}); ok {
		// @context is an array
		for _, iVal := range inArray {
			if valMap, ok := iVal.(map[string]interface{}); ok {
				// Element is a JSON Object (dictionary)
				for alias, val := range valMap {
					if s, ok := val.(string); ok {
						var n []RDFNode
						n, err = registry.getAliased(alias, s)
						if err != nil {
							return
						}
						nodes = append(nodes, n...)
					} else if aliasedMap, ok := val.(map[string]interface{}); ok {
						var n []RDFNode
						n, err = registry.getAliasedObject(alias, aliasedMap)
						if err != nil {
							return
						}
						nodes = append(nodes, n...)
					} else {
						err = fmt.Errorf("@context value in dict in array is neither a dict nor a string")
						return
					}
				}
			} else if s, ok := iVal.(string); ok {
				// Element is a single value
				var n []RDFNode
				n, err = registry.getFor(s)
				if err != nil {
					return
				}
				nodes = append(nodes, n...)
			} else {
				err = fmt.Errorf("@context value in array is neither a dict nor a string")
				return
			}
		}
	} else if inMap, ok := i.(map[string]interface{}); ok {
		// @context is a JSON object (dictionary)
		for alias, iVal := range inMap {
			if s, ok := iVal.(string); ok {
				var n []RDFNode
				n, err = registry.getAliased(alias, s)
				if err != nil {
					return
				}
				nodes = append(nodes, n...)
			} else if aliasedMap, ok := iVal.(map[string]interface{}); ok {
				var n []RDFNode
				n, err = registry.getAliasedObject(alias, aliasedMap)
				if err != nil {
					return
				}
				nodes = append(nodes, n...)
			} else {
				err = fmt.Errorf("@context value in dict is neither a dict nor a string")
				return
			}
		}
	} else {
		// @context is a single value
		s, ok := i.(string)
		if !ok {
			err = fmt.Errorf("single @context value is not a string")
		}
		return registry.getFor(s)
	}
	return
}
