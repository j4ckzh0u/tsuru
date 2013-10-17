// Copyright 2013 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package app

import (
	"bytes"
	"github.com/globocom/tsuru/app/bind"
	"github.com/globocom/tsuru/log"
	"github.com/globocom/tsuru/queue"
	"github.com/globocom/tsuru/service"
	"labix.org/v2/mgo/bson"
	"launchpad.net/gocheck"
	stdlog "log"
	"net/http"
	"net/http/httptest"
	"strings"
)

func (s *S) TestHandleMessage(c *gocheck.C) {
	s.provisioner.PrepareOutput([]byte("exported"))
	a := App{
		Name: "nemesis",
		Units: []Unit{
			{
				Name:    "i-00800",
				State:   "started",
				Machine: 19,
			},
		},
		Env: map[string]bind.EnvVar{
			"http_proxy": {
				Name:   "http_proxy",
				Value:  "http://myproxy.com:3128/",
				Public: true,
			},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	msg := queue.Message{Action: regenerateApprc, Args: []string{a.Name}}
	handle(&msg)
	cmds := s.provisioner.GetCmds("", &a)
	c.Assert(cmds, gocheck.HasLen, 1)
	output := strings.Replace(cmds[0].Cmd, "\n", " ", -1)
	outputRegexp := `^cat > /home/application/apprc <<END # generated by tsuru.*`
	outputRegexp += `export http_proxy="http://myproxy.com:3128/" END $`
	c.Assert(output, gocheck.Matches, outputRegexp)
}

func (s *S) TestHandleMessageWithSpecificUnit(c *gocheck.C) {
	s.provisioner.PrepareOutput([]byte("exported"))
	a := App{
		Name: "nemesis",
		Units: []Unit{
			{
				Name:    "nemesis/0",
				State:   "started",
				Machine: 19,
			},
			{
				Name:    "nemesis/1",
				State:   "started",
				Machine: 20,
			},
			{
				Name:    "nemesis/2",
				State:   "started",
				Machine: 23,
			},
		},
		Env: map[string]bind.EnvVar{
			"http_proxy": {
				Name:   "http_proxy",
				Value:  "http://myproxy.com:3128/",
				Public: true,
			},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	msg := queue.Message{Action: regenerateApprc, Args: []string{a.Name, "nemesis/1"}}
	handle(&msg)
	cmds := s.provisioner.GetCmds("", &a)
	c.Assert(cmds, gocheck.HasLen, 1)
	output := strings.Replace(cmds[0].Cmd, "\n", " ", -1)
	outputRegexp := `^cat > /home/application/apprc <<END # generated by tsuru.*`
	outputRegexp += `export http_proxy="http://myproxy.com:3128/" END $`
	c.Assert(output, gocheck.Matches, outputRegexp)
}

func (s *S) TestHandleMessageErrors(c *gocheck.C) {
	var data = []struct {
		action      string
		args        []string
		unitName    string
		expectedLog string
	}{
		{
			action:      "unknown-action",
			args:        []string{"does not matter"},
			expectedLog: `Error handling "unknown-action": invalid action.`,
		},
		{
			action: startApp,
			args:   []string{"nemesis"},
			expectedLog: `Error handling "start-app" for the app "nemesis":` +
				` all units must be started.`,
		},
		{
			action: startApp,
			args:   []string{"totem", "totem/0", "totem/1"},
			expectedLog: `Error handling "start-app" for the app "totem":` +
				` all units must be started.`,
		},
		{
			action: regenerateApprc,
			args:   []string{"nemesis"},
			expectedLog: `Error handling "regenerate-apprc" for the app "nemesis":` +
				` all units must be started.`,
		},
		{
			action: regenerateApprc,
			args:   []string{"totem", "totem/0", "totem/1"},
			expectedLog: `Error handling "regenerate-apprc" for the app "totem":` +
				` all units must be started.`,
		},
		{
			action:      regenerateApprc,
			args:        []string{"unknown-app"},
			expectedLog: `Error handling "regenerate-apprc": app "unknown-app" does not exist.`,
		},
		{
			action:      regenerateApprc,
			expectedLog: `Error handling "regenerate-apprc": this action requires at least 1 argument.`,
		},
		{
			action: regenerateApprc,
			args:   []string{"marathon", "marathon/0"},
			expectedLog: `Error handling "regenerate-apprc" for the app "marathon":` +
				` units are in "error" state.`,
		},
		{
			action: regenerateApprc,
			args:   []string{"territories", "territories/0"},
			expectedLog: `Error handling "regenerate-apprc" for the app "territories":` +
				` units are in "down" state.`,
		},
	}
	var buf bytes.Buffer
	a := App{Name: "nemesis"}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	a = App{
		Name: "totem",
		Units: []Unit{
			{Name: "totem/0", State: "pending"},
			{Name: "totem/1", State: "started"},
		},
	}
	err = s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	a = App{Name: "marathon", Units: []Unit{{Name: "marathon/0", State: "error"}}}
	err = s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	a = App{Name: "territories", Units: []Unit{{Name: "territories/0", State: "down"}}}
	err = s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	log.SetLogger(stdlog.New(&buf, "", 0))
	for _, d := range data {
		message := queue.Message{Action: d.action}
		if len(d.args) > 0 {
			message.Args = d.args
		}
		handle(&message)
		defer message.Delete() // Sanity
	}
	content := buf.String()
	lines := strings.Split(content, "\n")
	for i, d := range data {
		var found bool
		for j := i; j < len(lines); j++ {
			if lines[j] == d.expectedLog {
				found = true
				break
			}
		}
		if !found {
			c.Errorf("\nWant: %q.\nGot:\n%s", d.expectedLog, content)
		}
	}
}

func (s *S) TestHandleRestartAppMessage(c *gocheck.C) {
	s.provisioner.PrepareOutput([]byte("started"))
	a := App{
		Name: "nemesis",
		Units: []Unit{
			{
				Name:    "i-00800",
				State:   "started",
				Machine: 19,
			},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	message := queue.Message{Action: startApp, Args: []string{a.Name}}
	handle(&message)
	restarts := s.provisioner.Restarts(&a)
	c.Assert(restarts, gocheck.Equals, 1)
}

func (s *S) TestHandleRegenerateAndRestart(c *gocheck.C) {
	s.provisioner.PrepareOutput([]byte("exported"))
	s.provisioner.PrepareOutput([]byte("started"))
	a := App{
		Name: "nemesis",
		Units: []Unit{
			{
				Name:    "i-00800",
				State:   "started",
				Machine: 19,
			},
		},
		Env: map[string]bind.EnvVar{
			"http_proxy": {
				Name:   "http_proxy",
				Value:  "http://myproxy.com:3128/",
				Public: true,
			},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	s.provisioner.Provision(&a)
	defer s.provisioner.Destroy(&a)
	msg := queue.Message{Action: RegenerateApprcAndStart, Args: []string{a.Name}}
	handle(&msg)
	cmds := s.provisioner.GetCmds("", &a)
	c.Assert(cmds, gocheck.HasLen, 3)
	output := strings.Replace(cmds[0].Cmd, "\n", " ", -1)
	outputRegexp := `^cat > /home/application/apprc <<END # generated by tsuru.*`
	outputRegexp += `export http_proxy="http://myproxy.com:3128/" END $`
	c.Assert(output, gocheck.Matches, outputRegexp)
	restarts := s.provisioner.Restarts(&a)
	c.Assert(restarts, gocheck.Equals, 1)
}

func (s *S) TestUnitListStarted(c *gocheck.C) {
	var tests = []struct {
		input    []Unit
		expected bool
	}{
		{
			[]Unit{
				{State: "started"},
				{State: "started"},
				{State: "started"},
			},
			true,
		},
		{nil, true},
		{
			[]Unit{
				{State: "started"},
				{State: "blabla"},
			},
			false,
		},
		{
			[]Unit{
				{State: "started"},
				{State: "unreachable"},
			},
			true,
		},
	}
	for _, t := range tests {
		l := unitList(t.input)
		if got := l.Started(); got != t.expected {
			c.Errorf("l.Started(): want %v. Got %v.", t.expected, got)
		}
	}
}

func (s *S) TestUnitListState(c *gocheck.C) {
	var tests = []struct {
		input    []Unit
		expected string
	}{
		{
			[]Unit{{State: "started"}, {State: "started"}}, "started",
		},
		{nil, ""},
		{
			[]Unit{{State: "started"}, {State: "pending"}}, "",
		},
		{
			[]Unit{{State: "error"}}, "error",
		},
		{
			[]Unit{{State: "pending"}}, "pending",
		},
	}
	for _, t := range tests {
		l := unitList(t.input)
		if got := l.State(); got != t.expected {
			c.Errorf("l.State(): want %q. Got %q.", t.expected, got)
		}
	}
}

func (s *S) TestEnqueueUsesInternalQueue(c *gocheck.C) {
	Enqueue(queue.Message{Action: "do-something"})
	dqueue, _ := qfactory.Get("default")
	_, err := dqueue.Get(1e6)
	c.Assert(err, gocheck.NotNil)
	msg, err := aqueue().Get(1e6)
	c.Assert(err, gocheck.IsNil)
	c.Assert(msg.Action, gocheck.Equals, "do-something")
	msg.Delete()
}

func (s *S) TestHandleBindServiceMessage(c *gocheck.C) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{"DATABASE_USER":"root","DATABASE_PASSWORD":"s3cr3t"}`))
	}))
	defer ts.Close()
	srvc := service.Service{Name: "mysql", Endpoint: map[string]string{"production": ts.URL}}
	err := srvc.Create()
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Services().Remove(bson.M{"_id": "mysql"})
	instance := service.ServiceInstance{Name: "my-mysql", ServiceName: "mysql", Teams: []string{s.team.Name}}
	instance.Create()
	defer s.conn.ServiceInstances().Remove(bson.M{"_id": "my-mysql"})
	a := App{
		Name: "nemesis",
		Units: []Unit{
			{
				Name:    "i-00800",
				State:   "started",
				Machine: 19,
			},
		},
	}
	err = s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	err = instance.AddApp(a.Name)
	c.Assert(err, gocheck.IsNil)
	err = s.conn.ServiceInstances().Update(bson.M{"name": instance.Name}, instance)
	c.Assert(err, gocheck.IsNil)
	message := queue.Message{Action: BindService, Args: []string{a.Name, a.Units[0].Name}}
	handle(&message)
	c.Assert(called, gocheck.Equals, true)
}

func (s *S) TestEnsureAppIsStartedUnknownUnits(c *gocheck.C) {
	a := App{
		Name:     "neon",
		Platform: "symfonia",
		Units: []Unit{
			{Name: "neon/0", State: "started"},
			{Name: "neon/1", State: "started"},
			{Name: "neon/3", State: "started"},
			{Name: "neon/5", State: "started"},
		},
	}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, gocheck.IsNil)
	defer s.conn.Apps().Remove(bson.M{"name": a.Name})
	msg := queue.Message{Action: regenerateApprc, Args: []string{"neon", "neon/2", "neon/4"}}
	_, err = ensureAppIsStarted(&msg)
	c.Assert(err, gocheck.NotNil)
	c.Assert(err, gocheck.ErrorMatches, "^.*unknown units in the message. Deleting it...$")
}
