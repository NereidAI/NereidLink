<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Rule Chain Editor

The rule chain editor is a visual canvas for message routing.

## Building a chain

1. Drag nodes from the left palette onto the canvas.
2. Drag from a node's output handle to another node to create an edge.
3. Click a node to configure it in the right drawer.

## Node types

- **Script Filter** — passes the message when the JavaScript condition returns `true`, for example `return msg.temperature > 30`.
- **Switch** — routes to named relations from a list of cases; unmatched messages follow the default relation.
- **Transform Script** — replaces the message with the returned object.
- **Save Attributes** — stores attributes returned by the script in the chosen scope.
- **Create Alarm / Clear Alarm** — opens or clears an alarm with type, severity and optional dedupe key.
- **Send Command** — publishes a command with parameters returned by the script.
- **Forward** — queues the message for a tenant [data forwarder](help:forwarders); delivery is asynchronous.
- **Log** — emits a log line.

## JavaScript rules

Scripts run in an embedded JavaScript engine. You must explicitly `return` a value. The incoming message is available as `msg` and metadata as `metadata`.

## Testing

Enter a JSON test payload and click **Test delivery**. The chain executes without saving; visited nodes are highlighted, and failed nodes show the error. This lets you validate logic before committing a version.

## Saving

- **Save new version** — stores the current graph as a new draft.
- **Save & activate** — saves and immediately makes it live; the previous active version is archived.

## Live trace

Use **Live trace** to watch real telemetry flow through the active version. It opens a bottom console:

1. Turn on **Capture real messages**. Tracing only applies to the currently active version; activate the chain first if it has never been activated.
2. Send real device data (for example with the device simulator).
3. Each message appears in the left list with its time, node count and error count. Select one to see the ordered node path on the right.
4. Every node step shows its type, execution time and the relations it took; a failed step shows the error in red. The visited path is highlighted on the canvas.
5. Click a step's node name to center that node.

The console refreshes automatically while capturing. Closing the console does not stop tracing; turn the switch off to stop collecting.
