// Package status decouples synchronization work from its presentation.
//
// Interactive terminal runs use Bubble Tea to show the current account,
// mailbox, operation, and running add/delete counts. Non-interactive callers
// use RunPlain, which executes the task synchronously and emits line-oriented
// messages. The synchronous path is important for programs such as mu4e that
// must not continue until MailSalonSync has completely finished.
package status
