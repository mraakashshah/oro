package data

import "time"

type Msg any

type Cmd func() Msg

func tick(after time.Duration, fn func(time.Time) Msg) Cmd {
	return func() Msg {
		time.Sleep(after)
		return fn(time.Now())
	}
}
