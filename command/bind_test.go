package command_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/cleanup"
	"github.com/ryan053857/mygoravel/command"
)

// greeter 是测试用的 action：构造函数只保存依赖。
type greeter struct{ prefix string }

func (g *greeter) Command(cmd *cobra.Command, args []string) error {
	loud, err := cmd.Flags().GetBool("loud")
	if err != nil {
		return err
	}
	msg := g.prefix + " " + args[0]
	if loud {
		msg = strings.ToUpper(msg)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), msg)
	return err
}

type app struct{ prefix string }

// bindFixture 返回绑定到 app 的 greet hello 命令，events 记录 open、declare 和关闭的顺序。
func bindFixture(events *[]string, openErr error) []*cobra.Command {
	open := func(_ context.Context, cfg settings, stack *cleanup.Stack) (*app, error) {
		*events = append(*events, "open "+cfg.Name)
		if openErr != nil {
			return nil, openErr
		}
		stack.MustAdd("app", func(context.Context) error {
			*events = append(*events, "close")
			return nil
		})
		return &app{prefix: "hello from " + cfg.Name + ","}, nil
	}
	return command.Bind(open, func(a *app) []*cobra.Command {
		*events = append(*events, fmt.Sprintf("declare %q", a.prefix))
		hello := &cobra.Command{Use: "hello <name>", Args: command.ExactArgs(1), RunE: (&greeter{prefix: a.prefix}).Command}
		hello.Flags().Bool("loud", false, "大写输出")
		group := &cobra.Command{Use: "greet"}
		group.AddCommand(hello)
		return []*cobra.Command{group}
	})
}

func TestBindAssemblesDependenciesOnExecution(t *testing.T) {
	t.Parallel()
	var events []string
	root := newRoot(bindFixture(&events, nil)...)
	if want := []string{`declare ""`}; !slices.Equal(events, want) {
		t.Fatalf("events after declaring = %q, want %q", events, want)
	}

	out, _, err := execute(root, "greet", "hello", "alice", "--loud", "--config", writeConfig(t, minimalConfig))
	if err != nil {
		t.Fatal(err)
	}
	if out != "HELLO FROM BLOG, ALICE\n" {
		t.Fatalf("stdout = %q", out)
	}
	want := []string{`declare ""`, "open blog", `declare "hello from blog,"`, "close"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %q, want %q", events, want)
	}
}

func TestBindHelpDoesNotOpen(t *testing.T) {
	t.Parallel()
	var events []string
	root := newRoot(bindFixture(&events, nil)...)
	if _, _, err := execute(root, "greet", "hello", "--help"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %q, want only the declaration", events)
	}
}

func TestBindReportsErrors(t *testing.T) {
	t.Parallel()
	t.Run("args", func(t *testing.T) {
		var events []string
		_, _, err := execute(newRoot(bindFixture(&events, nil)...), "greet", "hello")
		if command.ExitCode(err) != command.ExitUsage || len(events) != 1 {
			t.Fatalf("err = %v, code = %d, events = %q", err, command.ExitCode(err), events)
		}
	})
	t.Run("config", func(t *testing.T) {
		var events []string
		_, _, err := execute(newRoot(bindFixture(&events, nil)...), "greet", "hello", "alice", "--config", writeConfig(t, "name: \n"))
		if command.ExitCode(err) != command.ExitConfig || len(events) != 1 {
			t.Fatalf("err = %v, code = %d, events = %q", err, command.ExitCode(err), events)
		}
	})
	t.Run("open", func(t *testing.T) {
		var events []string
		boom := errors.New("database down")
		_, _, err := execute(newRoot(bindFixture(&events, boom)...), "greet", "hello", "alice", "--config", writeConfig(t, minimalConfig))
		if !errors.Is(err, boom) || !slices.Equal(events, []string{`declare ""`, "open blog"}) {
			t.Fatalf("err = %v, events = %q", err, events)
		}
	})
}
