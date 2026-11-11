<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Business orchestration

[Business orchestration](help:glossary/business-orchestration) assembles a business from existing assets. It does not execute camera frames and does not replace the NereidInference pipeline editor or registry.

## Templates and instances

The **Templates** tab lists immutable [business template versions](help:glossary/business-template-version). The platform ships no business vocabulary of its own: versions exist per tenant only after a pack import or a designer publish. Select **Import pack** to upload a YAML or JSON template pack — either a bare template document or a `{"templates":[...]}` envelope — or **Create template** to start a blank assembly. Enter a unique ID and version, edit the business canvas and scalar parameter JSON Schema, then save. Select **Export pack** to download every tenant template as one portable YAML pack. The reference packs (**Face access control `0.0.1`** and **Meter reading `1.0.0`**) ship as data files under `samples/business-templates/`, not as compiled-in templates.

Select **Edit** on any card to load its document, keep its template ID fixed, and publish the changes as a new immutable version. The designer suggests the next patch version, which you can change before saving. Existing instances remain pinned to their original snapshots. Select **Delete** to remove an unreferenced version after confirmation; the server rejects deletion with `409` while any instance references the version. To remove a referenced version, first undeploy and delete every referencing instance; deleting an instance never deletes its template version.

Select **Create instance** on a version card and enter a name to open an independent draft on the **Canvas** tab. Saving the draft creates a [business instance](help:glossary/business-instance) with the complete original template snapshot, so publishing another template version does not change it. Each saved desired revision is a [business generation](help:glossary/business-generation). Templates can contain unbound asset placeholders; select the real cameras, models, companion conclusion devices, commands, and forwarders required by the assembly before deployment. Face enrollment and meter calibration remain in their companion services.

The reference **Face access control `0.0.1`** template pack has two independent model dependencies: **face detector** (YuNet) and **face embedding** (SFace). Select a concrete READY model ID/version for each on the target gateway; enrollment does not add a model per person. NI owns detection decoding, five-point alignment, crop batching, and embedding facts. The face companion owns enrollment, the gallery, and 1:N matching. These two NL model nodes do not define an NI pipeline topology.

## Assembly canvas

Drag asset types from the palette and connect their business relationships:

- camera to pipeline;
- model or external servable to pipeline;
- pipeline to companion or rule;
- companion to rule or northbound output;
- rule to action or northbound output.

These edges mean subscription or trigger relationships. Do not draw detection, crop, OCR, embedding, or other per-frame stages here.

Select a node to edit its asset references. A camera node loads existing active gateways and then lists the devices managed by the selected gateway; its output device uses the same dependent registry. Changing or clearing the gateway clears the previous camera and output selections. If either list is empty, create the gateway or managed camera on the **Devices** page first. Source kind, decode acceleration, and publish mode are closed selectors; target-device presets still permit a custom OpenVINO target.

Model and companion selectors derive their target gateways from the connected camera → pipeline relationships and the model/companion's pipeline edges. A model ID/version is offered only when it is READY and non-stale on every target gateway; version selection depends on model ID. A companion is offered only when an enabled endpoint is registered on every target gateway. Changing those relationships or a camera gateway clears the affected model ID/version or companion selection. Conclusion devices and command targets come from the active device registry.

Actions use a closed type selector. **Send command** shows a target-device selector and then the enabled commands declared by that target; changing the target clears the command. **Create alarm** instead shows alarm type and the closed severity values `CRITICAL`, `MAJOR`, `MINOR`, and `WARNING`. Northbound outputs list the tenant's enabled forwarders. A retained reference that is absent from the current catalog remains visible so an imported draft can be inspected and repaired, but it is not proof of deployability.

Pipeline IDs remain exact manual references because this console has no authoritative pipeline catalog. Raw EdgeX device/profile/resource names likewise remain exact manual values rather than fabricated dropdown choices. Edit scalar template parameters in the parameter section. When editing an existing instance, **Save draft** sends the expected generation and rejects stale-editor overwrites.

**Validate** shows document and live model issues. **Preview expansion** shows the exact declarative artifacts and deterministic digest for the saved generation; see [business expansion](help:glossary/business-expansion). **Validate**, **Preview expansion**, and **Deploy** save pending canvas edits first, so they do not run against an older saved assembly.

## Publishing and privacy

`PublishMode` and `PublishReadings` control local NI event emission. They do not upload data by themselves. Add an explicit northbound node, select an existing forwarder and conclusion fields, and enter the exact raw EdgeX device name, profile name, and resource names reported by the target gateway. NereidLink never derives those EdgeX names from its own device UUIDs or canonical names.

The reading whitelist and EdgeX resource whitelist are both explicit. Wildcards, `PipelineJSON`, embedding paths, and case-sensitive `Embedding*` resources cannot be used for northbound publication. During expansion, each northbound node remains a channel contribution and NereidLink computes one gateway-level `edge-northbound-filter` singleton from all active or pending business instances. The filter uses ordered allow/deny rules with raw EdgeX names; when no contributor remains, it is quiesced rather than deleted. The `channel` field is envelope metadata, while the forwarder still selects the JetStream stream and sink. A binding consumed by a companion cannot use `PublishMode=nonempty`, and an external-servable binding cannot set `TargetDevice`.

## Deploy and monitor

Deployment preflights every destination before changing NL-owned resources. Missing pipeline, companion, or transport capability fails the deployment instead of producing a partial success. Connector specs, rule chains, device bindings, and forwarders retain their existing owners.

The **Instances** tab shows desired, pending, and active generations, validation issues, health, rollback, and undeploy actions. A generation becomes active only after health read-back. Rollback selects a retained expansion snapshot. Undeploy removes resources owned by the instance; it does not unload shared models or delete companions and forwarders.

Select **Monitor** on an instance to read per-camera health. Metrics such as P50/P95 latency and event rate appear only when the runtime reports them. “Unavailable” means there is no authoritative reading; it is not zero.

## Current M3 gate

Pipeline and reference-only external-servable artifacts use the signed `edge-manager-resource` v1 connector contract and require current observed manager readback before becoming healthy. Managed external-servable definitions remain blocked until NI supplies a runtime-only symmetric resource. Deployment of companion-backed assemblies also remains blocked while the companion subscription profile is unavailable. Retained development snapshots containing the older flat manager artifact are invalid and must be saved as a newly expanded generation rather than rolled back or deployed. See the [gateway integration](help:page/integrate-gateway) guide and the design and sign-off documents in the NereidLink `docs/` directory for integration details.
