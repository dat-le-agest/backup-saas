package di

import (
	"fmt"
	"reflect"
	"sync"
)

type Scope int

const (
	Singleton Scope = iota
	Transient
)

var errorType = reflect.TypeOf((*error)(nil)).Elem()

type providerNode struct {
	fn       reflect.Value
	scope    Scope
	hasError bool
}

type Container struct {
	mu        sync.Mutex
	providers map[reflect.Type]*providerNode
	instances map[reflect.Type]interface{}
	resolving map[reflect.Type]bool
}

func New() *Container {
	return &Container{
		providers: make(map[reflect.Type]*providerNode),
		instances: make(map[reflect.Type]interface{}),
		resolving: make(map[reflect.Type]bool),
	}
}

// Provide registers a constructor for type T.
// provider must be func(deps...) X or func(deps...) (X, error), where X is assignable to T.
// This allows registering a concrete constructor under an interface type.
func Provide[T any](c *Container, provider interface{}, scope Scope) error {
	targetType := reflect.TypeOf((*T)(nil)).Elem()

	val := reflect.ValueOf(provider)
	if val.Kind() != reflect.Func {
		return fmt.Errorf("provider for %v must be a function", targetType)
	}

	typ := val.Type()
	hasError := false
	switch {
	case typ.NumOut() == 1:
	case typ.NumOut() == 2 && typ.Out(1) == errorType:
		hasError = true
	default:
		return fmt.Errorf("provider for %v must return (T) or (T, error)", targetType)
	}

	if !typ.Out(0).AssignableTo(targetType) {
		return fmt.Errorf("provider returns %v, not assignable to %v", typ.Out(0), targetType)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isRegistered(targetType) {
		return fmt.Errorf("provider for type %v already exists", targetType)
	}

	c.providers[targetType] = &providerNode{
		fn:       val,
		scope:    scope,
		hasError: hasError,
	}
	return nil
}

// Instance registers an already-built value as a singleton of type T.
// Used to bridge infrastructure owned by sctx (db, cache, logger...) into the container.
func Instance[T any](c *Container, value T) error {
	targetType := reflect.TypeOf((*T)(nil)).Elem()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isRegistered(targetType) {
		return fmt.Errorf("provider for type %v already exists", targetType)
	}

	c.instances[targetType] = value
	return nil
}

func (c *Container) isRegistered(t reflect.Type) bool {
	_, hasProvider := c.providers[t]
	_, hasInstance := c.instances[t]
	return hasProvider || hasInstance
}

// Build validates the graph: every dependency is registered and there is no cycle.
func (c *Container) Build() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	inDegree := make(map[reflect.Type]int)
	graph := make(map[reflect.Type][]reflect.Type)

	for targetType, node := range c.providers {
		if _, exists := inDegree[targetType]; !exists {
			inDegree[targetType] = 0
		}

		providerType := node.fn.Type()
		for i := 0; i < providerType.NumIn(); i++ {
			paramType := providerType.In(i)
			if _, isInstance := c.instances[paramType]; isInstance {
				continue
			}
			if _, exists := c.providers[paramType]; !exists {
				return fmt.Errorf("missing provider for type %v (required by %v)", paramType, targetType)
			}
			graph[paramType] = append(graph[paramType], targetType)
			inDegree[targetType]++
		}
	}

	queue := []reflect.Type{}
	for node, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, node)
		}
	}

	visitedCount := 0
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		visitedCount++

		for _, neighbor := range graph[curr] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if visitedCount != len(c.providers) {
		return fmt.Errorf("circular dependency detected among providers")
	}

	return nil
}

// resolveType must be called with c.mu held.
func (c *Container) resolveType(targetType reflect.Type) (interface{}, error) {
	if instance, ok := c.instances[targetType]; ok {
		return instance, nil
	}

	node, exists := c.providers[targetType]
	if !exists {
		return nil, fmt.Errorf("no provider registered for type: %v", targetType)
	}

	if c.resolving[targetType] {
		return nil, fmt.Errorf("circular dependency detected at type: %v", targetType)
	}
	c.resolving[targetType] = true
	defer delete(c.resolving, targetType)

	providerType := node.fn.Type()
	args := make([]reflect.Value, providerType.NumIn())
	for i := 0; i < providerType.NumIn(); i++ {
		paramType := providerType.In(i)
		paramVal, err := c.resolveType(paramType)
		if err != nil {
			return nil, err
		}
		if paramVal == nil {
			args[i] = reflect.Zero(paramType)
		} else {
			args[i] = reflect.ValueOf(paramVal)
		}
	}

	results := node.fn.Call(args)
	if node.hasError {
		if errVal := results[1].Interface(); errVal != nil {
			return nil, fmt.Errorf("provide %v: %w", targetType, errVal.(error))
		}
	}
	instance := results[0].Interface()

	if node.scope == Singleton {
		c.instances[targetType] = instance
	}

	return instance, nil
}

func Get[T any](c *Container) (T, error) {
	var zero T
	targetType := reflect.TypeOf((*T)(nil)).Elem()

	c.mu.Lock()
	defer c.mu.Unlock()

	val, err := c.resolveType(targetType)
	if err != nil {
		return zero, err
	}
	if val == nil {
		return zero, nil
	}

	return val.(T), nil
}

func MustGet[T any](c *Container) T {
	v, err := Get[T](c)
	if err != nil {
		panic(err)
	}
	return v
}
