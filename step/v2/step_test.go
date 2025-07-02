package step

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipeline(t *testing.T) {
	t.Parallel()
	var result int
	err := Pipeline("test", func(ctx context.Context) {
		one := Func01("one", func(context.Context) int {
			return 1
		})(ctx)
		result = Func11("+2", func(_ context.Context, one int) int {
			return one + 2
		})(ctx, one)
	})
	require.NoError(t, err)
	assert.Equal(t, 3, result)
}

func TestCmd(t *testing.T) {
	t.Parallel()
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		var txt string
		err := Pipeline("cmd", func(ctx context.Context) {
			txt = Cmd(ctx, "echo", "hello, world")
		})
		require.NoError(t, err)
		assert.Equal(t, "hello, world\n", txt)
	})

	t.Run("failure", func(t *testing.T) {
		t.Parallel()
		err := Pipeline("cmd", func(ctx context.Context) {
			Cmd(ctx, "does-not-exist-so-this-should-fail")
		})
		require.Error(t, err)
	})
}

func TestNestedError(t *testing.T) {
	t.Parallel()
	expectedErr := fmt.Errorf("an error")
	err := Pipeline("test", func(ctx context.Context) {
		Func00("n1", func(ctx context.Context) {
			Func00("n2", func(ctx context.Context) {
				Func00E("n3", func(ctx context.Context) error {
					return expectedErr
				})(ctx)
			})(ctx)
		})(ctx)
	})
	require.ErrorIs(t, err, expectedErr)
}

func TestEnv(t *testing.T) {
	var result string
	err := Pipeline("test", func(ctx context.Context) {
		result = Cmd(WithEnv(ctx, &EnvVar{
			Key:   "ENV",
			Value: "result",
		}), "bash", "-c", "echo $ENV")
	})
	require.NoError(t, err)
	assert.Equal(t, "result\n", result)
}

func TestCast(t *testing.T) {
	t.Parallel()
	type Foo struct {
		Fizz int
		Buzz string
	}

	t.Run("nil", func(t *testing.T) {
		testCastNil[*Foo](t)
		testCastNil[*int](t)
		testCastNil[Foo](t)
		testCastNil[[]string](t)
		testCastNil[float64](t)
		testCastNil[int](t)
		testCastNil[map[int]float64](t)
		testCastNil[string](t)
	})

	t.Run("some", func(t *testing.T) {
		testCastNonNil(t, &Foo{Buzz: "bzzz"})
		testCastNonNil(t, ref(3))
		testCastNonNil(t, Foo{Fizz: 3})
		testCastNonNil(t, []string{"a", "b"})
		testCastNonNil(t, 2.0)
		testCastNonNil(t, 7)
		testCastNonNil(t, map[string]*int{"three": ref(3)})
		testCastNonNil(t, "fizzbuzz")
	})
}

func ref[T any](t T) *T { return &t }

func testCastNil[T any](t *testing.T) {
	var zero T
	var untyped any
	t.Run(reflect.TypeOf(zero).String(), func(t *testing.T) {
		t.Parallel()
		var actual T
		assert.NotPanics(t, func() { actual = cast[T](untyped) })
		assert.Equal(t, zero, actual)
	})
}

func testCastNonNil[T any](t *testing.T, expected T) {
	var untyped any = expected
	t.Run(reflect.TypeOf(expected).String(), func(t *testing.T) {
		t.Parallel()
		var actual T
		assert.NotPanics(t, func() { actual = cast[T](untyped) })
		assert.Equal(t, expected, actual)
	})
}
