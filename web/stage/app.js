const statusNode = document.getElementById("status");
const summaryNode = document.getElementById("summary");
const eventLogNode = document.getElementById("event-log");
const avatarLabelNode = document.getElementById("avatar-label");
const workflowLabelNode = document.getElementById("workflow-label");
const avatarCircleNode = document.getElementById("avatar-node");
const runSelectorNode = document.getElementById("run-selector");
const runSelectionStatusNode = document.getElementById("run-selection-status");
const runTimelineNode = document.getElementById("run-timeline");
const runLLMTitleNode = document.getElementById("run-llm-title");
const runTelemetryTitleNode = document.getElementById("run-telemetry-title");
const configuredLLMNode = document.getElementById("configured-llm");
const latestRunLLMNode = document.getElementById("latest-run-llm");
const latestTelemetryNode = document.getElementById("latest-telemetry");
const taskFeedbackNode = document.getElementById("task-feedback");
const skillGovernanceSelectorNode = document.getElementById("skill-governance-selector");
const skillGovernancePrevNode = document.getElementById("skill-governance-prev");
const skillGovernanceNextNode = document.getElementById("skill-governance-next");
const skillGovernancePrevSectionNode = document.getElementById("skill-governance-prev-section");
const skillGovernanceNextSectionNode = document.getElementById("skill-governance-next-section");
const skillGovernanceSelectionStatusNode = document.getElementById("skill-governance-selection-status");
const skillGovernanceSectionSummaryNode = document.getElementById("skill-governance-section-summary");
const skillGovernanceSectionOverviewNode = document.getElementById("skill-governance-section-overview");
const skillGovernanceNode = document.getElementById("skill-governance");
const llmDiffFilterNode = document.getElementById("llm-diff-filter");
const llmDiffSummaryNode = document.getElementById("llm-diff-summary");
const llmDiffNode = document.getElementById("llm-diff");
const llmDiffFieldsNode = document.getElementById("llm-diff-fields");

let selectionMode = "latest";
let pinnedRunId = "";
let displayedRunId = "";
let selectedGovernanceItemKey = "";
let diffFilter = "changed";
let latestOperatorSnapshot = null;
let expandedTaskFeedbackSectionKey = "";

function currentRunLabelPrefix() {
  return selectionMode === "fixed" ? "Selected Run" : "Latest Run";
}

function isActiveRun(runId) {
  if (selectionMode === "fixed") {
    return pinnedRunId === runId;
  }
  return displayedRunId === runId;
}

function currentHistoryRunId() {
  if (selectionMode === "fixed") {
    return pinnedRunId;
  }
  return displayedRunId;
}

function buildOperatorPath() {
  const params = new URLSearchParams();
  if (selectionMode === "fixed" && pinnedRunId) {
    params.set("run_id", pinnedRunId);
  }
  if (selectedGovernanceItemKey) {
    params.set("skill_item", selectedGovernanceItemKey);
  }
  const query = params.toString();
  if (!query) {
    return "/api/operator";
  }
  return `/api/operator?${query}`;
}

function buildHistoryPath() {
  const runId = currentHistoryRunId();
  if (!runId) {
    return "/api/events";
  }
  return `/api/events?run_id=${encodeURIComponent(runId)}`;
}

function shorten(text, limit = 72) {
  if (!text || text.length <= limit) {
    return text;
  }
  return `${text.slice(0, limit - 3)}...`;
}

function describeRun(run) {
  const detail = run.summary || run.input || run.run_id;
  const status = run.status || "running";
  return `${status} | ${shorten(detail)}`;
}

function formatTimestamp(timestamp) {
  if (!timestamp) {
    return "n/a";
  }
  const value = new Date(timestamp);
  if (Number.isNaN(value.getTime())) {
    return timestamp;
  }
  return value.toLocaleString();
}

function renderRunTimeline(runs) {
  runTimelineNode.textContent = "";

  if (!Array.isArray(runs) || runs.length === 0) {
    runTimelineNode.textContent = "No runs yet.";
    return;
  }

  runs.forEach((run) => {
    const card = document.createElement("button");
    card.type = "button";
    card.className = `run-card ${isActiveRun(run.run_id) ? "run-card-active" : ""}`.trim();
    card.dataset.state = run.status || "running";

    const header = document.createElement("div");
    header.className = "run-card-header";

    const title = document.createElement("div");
    title.className = "run-card-title";
    title.textContent = run.run_id;
    header.appendChild(title);

    const badge = document.createElement("div");
    badge.className = "run-card-badge";
    badge.textContent = isActiveRun(run.run_id) ? (selectionMode === "fixed" ? "viewed" : "latest") : (run.status || "running");
    header.appendChild(badge);

    card.appendChild(header);

    const meta = document.createElement("div");
    meta.className = "run-card-meta";
    meta.textContent = `${run.provider || "provider n/a"} | ${run.model || "model n/a"}`;
    card.appendChild(meta);

    const timing = document.createElement("div");
    timing.className = "run-card-meta";
    timing.textContent = `started: ${formatTimestamp(run.started_at)} | updated: ${formatTimestamp(run.updated_at)}`;
    card.appendChild(timing);

    const counters = document.createElement("div");
    counters.className = "run-card-counters";
    counters.textContent = `events ${run.event_count} | llm ${run.llm_requests} | tools ${run.tool_requests} | verify ${run.verification_runs} | duration ${run.duration_ms}ms`;
    card.appendChild(counters);

    const summary = document.createElement("div");
    summary.className = "run-card-summary";
    summary.textContent = shorten(run.summary || run.input || "No run summary yet.", 160);
    card.appendChild(summary);

    card.addEventListener("click", async () => {
      runSelectorNode.value = run.run_id;
      selectionMode = "fixed";
      pinnedRunId = run.run_id;
      await reloadStage();
    });

    runTimelineNode.appendChild(card);
  });
}

function applyRunTitles() {
  runLLMTitleNode.textContent = `${currentRunLabelPrefix()} LLM`;
  runTelemetryTitleNode.textContent = `${currentRunLabelPrefix()} Telemetry`;
}

function renderRunSelector(runs, selectedRunId) {
  if (selectionMode === "fixed" && pinnedRunId && !runs.some((run) => run.run_id === pinnedRunId)) {
    selectionMode = "latest";
    pinnedRunId = "";
  }

  runSelectorNode.innerHTML = "";

  const latestOption = document.createElement("option");
  latestOption.value = "";
  latestOption.textContent = "Follow latest run";
  runSelectorNode.appendChild(latestOption);

  runs.forEach((run) => {
    const option = document.createElement("option");
    option.value = run.run_id;
    option.textContent = describeRun(run);
    runSelectorNode.appendChild(option);
  });

  displayedRunId = selectedRunId || "";
  if (selectionMode === "fixed" && pinnedRunId) {
    runSelectorNode.value = pinnedRunId;
    runSelectionStatusNode.textContent = `Pinned run: ${pinnedRunId}`;
  } else {
    runSelectorNode.value = "";
    runSelectionStatusNode.textContent = displayedRunId ? `Following latest run: ${displayedRunId}` : "No run history yet.";
  }

  applyRunTitles();
}

function resetEventLog() {
  eventLogNode.textContent = "";
}

function resetDiffFields() {
  llmDiffFieldsNode.textContent = "";
}

function isVisibleEvent(event) {
  const runId = currentHistoryRunId();
  if (!runId) {
    return true;
  }
  return event.run_id === runId;
}

function renderLLMStatus(status, warning) {
  if (!status && !warning) {
    return "unavailable";
  }
  if (!status) {
    return `warning: ${warning}`;
  }
  const lines = [
    `provider: ${status.name}`,
    `family: ${status.family}`,
    `model: ${status.model}`,
    `think: ${status.think_mode}`,
    `web search: ${status.web_search_enabled ? "enabled" : status.supports_web_search ? "supported" : "off"}`,
    `local: ${status.local ? "yes" : "no"}`,
  ];
  if (status.base_url) {
    lines.push(`base URL: ${status.base_url}`);
  }
  if (status.api_key_env) {
    lines.push(`api key env: ${status.api_key_env}`);
  }
  if (warning) {
    lines.push(`warning: ${warning}`);
  }
  return lines.join("\n");
}

function renderTelemetry(snapshot) {
  if (!snapshot) {
    return "unavailable";
  }
  const lines = [
    `mode: ${snapshot.mode}`,
    `status: ${snapshot.status}`,
    `provider: ${snapshot.provider || "n/a"}`,
    `model: ${snapshot.model || "n/a"}`,
    `duration: ${snapshot.duration_ms}ms`,
    `llm: ${snapshot.llm_requests}/${snapshot.llm_completions}/${snapshot.llm_failures}`,
    `tools: ${snapshot.tool_requests}/${snapshot.tool_completions}/${snapshot.tool_failures}`,
  ];
  if (snapshot.usage_known) {
    lines.push(`tokens: ${snapshot.prompt_tokens}/${snapshot.completion_tokens}/${snapshot.total_tokens}`);
  } else {
    lines.push("tokens: unavailable");
  }
  return lines.join("\n");
}

function taskFeedbackSectionLines(snapshot, sectionKey) {
  if (!snapshot) {
    return [];
  }
  if (sectionKey === "collaboration") {
    const collaboration = snapshot.collaboration || null;
    if (!collaboration) {
      return [];
    }
    const lines = [];
    if (collaboration.focus_cue) {
      lines.push(`priority cue: ${collaboration.focus_cue}`);
    }
    if (Array.isArray(collaboration.execution_chain) && collaboration.execution_chain.length > 0) {
      lines.push(`execution chain: ${collaboration.execution_chain.length}`);
      collaboration.execution_chain.forEach((step) => lines.push(`chain step: ${step}`));
    }
    if (collaboration.latest_avatar_report) {
      lines.push(`latest avatar report: ${collaboration.latest_avatar_report}`);
      if (collaboration.latest_avatar_report_route) {
        lines.push(`latest avatar report route: ${collaboration.latest_avatar_report_route}`);
      }
    }
    if (collaboration.latest_avatar_ask) {
      lines.push(`latest avatar ask: ${collaboration.latest_avatar_ask}`);
      if (collaboration.latest_avatar_ask_route) {
        lines.push(`latest avatar ask route: ${collaboration.latest_avatar_ask_route}`);
      }
      if (collaboration.latest_avatar_ask_status) {
        lines.push(`latest avatar ask status: ${collaboration.latest_avatar_ask_status}`);
      }
    }
    if (collaboration.latest_avatar_follow_up) {
      lines.push(`latest avatar follow-up: ${collaboration.latest_avatar_follow_up}`);
      if (collaboration.latest_avatar_follow_up_route) {
        lines.push(`latest avatar follow-up route: ${collaboration.latest_avatar_follow_up_route}`);
      }
    }
    if (collaboration.latest_avatar_challenge) {
      lines.push(`latest avatar challenge: ${collaboration.latest_avatar_challenge}`);
      if (collaboration.latest_avatar_challenge_route) {
        lines.push(`latest avatar challenge route: ${collaboration.latest_avatar_challenge_route}`);
      }
    }
    if (collaboration.latest_avatar_summary) {
      lines.push(`latest avatar summary: ${collaboration.latest_avatar_summary}`);
      if (collaboration.latest_avatar_summary_route) {
        lines.push(`latest avatar summary route: ${collaboration.latest_avatar_summary_route}`);
      }
    }
    return lines;
  }
  if (sectionKey === "feedback") {
    const lines = [
      `evaluations: ${snapshot.evaluation_count}`,
      `warm lessons: ${snapshot.warm_lesson_count}`,
      `evolution candidates: ${snapshot.evolution_count}`,
      `project lessons: ${snapshot.project_lesson_count}`,
    ];
    if (snapshot.latest_evaluation) {
      lines.push(`latest evaluation: ${snapshot.latest_evaluation}`);
    }
    if (snapshot.latest_warm_lesson) {
      lines.push(`latest warm lesson: ${snapshot.latest_warm_lesson}`);
    }
    if (snapshot.latest_evolution) {
      lines.push(`latest evolution: ${snapshot.latest_evolution}`);
    }
    if (snapshot.top_project_lesson) {
      lines.push(`top project lesson: ${snapshot.top_project_lesson}`);
    }
    return lines;
  }
  if (sectionKey === "workflow-node") {
    const lines = [];
    if (snapshot.workflow_node_next_action) {
      lines.push(`workflow node next action: ${snapshot.workflow_node_next_action}`);
    }
    if (snapshot.workflow_node_verifier_report_path) {
      lines.push(`workflow node verifier report: ${snapshot.workflow_node_verifier_report_path}`);
    }
    lines.push("workflow node evidence is read-only inspection metadata");
    return lines;
  }
  if (sectionKey === "verification") {
    const lines = [];
    if (snapshot.verification) {
      lines.push(`verification: ${snapshot.verification.verdict} via ${snapshot.verification.tool || "n/a"}`);
      if (snapshot.verification.summary) {
        lines.push(`verification summary: ${snapshot.verification.summary}`);
      }
      if (snapshot.verification.follow_up_command) {
        lines.push(`verification follow-up: ${snapshot.verification.follow_up_command}`);
      }
    }
    if (snapshot.latest_non_pass_verification) {
      lines.push(`latest non-pass verification: ${snapshot.latest_non_pass_verification}`);
      if (snapshot.latest_non_pass_follow_up) {
        lines.push(`latest non-pass follow-up: ${snapshot.latest_non_pass_follow_up}`);
      }
    }
    if (snapshot.reverify_status) {
      lines.push(`reverify status: ${snapshot.reverify_status}`);
    }
    if (snapshot.latest_reverify_attempt) {
      lines.push(`latest reverify attempt: ${snapshot.latest_reverify_attempt}`);
    }
    if (snapshot.latest_reverify_attempt_proposal_id) {
      lines.push(`latest reverify attempt proposal: ${snapshot.latest_reverify_attempt_proposal_id}`);
    }
    if (Array.isArray(snapshot.latest_reverify_attempt_targets) && snapshot.latest_reverify_attempt_targets.length > 0) {
      lines.push(`latest reverify attempt targets: ${snapshot.latest_reverify_attempt_targets.length}`);
      snapshot.latest_reverify_attempt_targets.forEach((target) => lines.push(`latest reverify attempt target: ${target}`));
    }
    if (snapshot.latest_reverify_remediation) {
      lines.push(`latest reverify remediation: ${snapshot.latest_reverify_remediation}`);
    }
    if (snapshot.latest_reverify_remediation_proposal_id) {
      lines.push(`latest reverify remediation proposal: ${snapshot.latest_reverify_remediation_proposal_id}`);
    }
    if (Array.isArray(snapshot.latest_reverify_remediation_targets) && snapshot.latest_reverify_remediation_targets.length > 0) {
      lines.push(`latest reverify remediation targets: ${snapshot.latest_reverify_remediation_targets.length}`);
      snapshot.latest_reverify_remediation_targets.forEach((target) => lines.push(`latest reverify remediation target: ${target}`));
    }
    if (snapshot.latest_reverify_closure) {
      lines.push(`latest reverify closure: ${snapshot.latest_reverify_closure}`);
    }
    if (snapshot.task_status) {
      lines.push(`task status: ${snapshot.task_status}`);
    }
    if (snapshot.latest_approval_required) {
      lines.push(`latest approval required: ${snapshot.latest_approval_required}`);
      if (snapshot.latest_approval_required_source) {
        lines.push(`latest approval required source: ${snapshot.latest_approval_required_source}`);
      }
      if (snapshot.latest_approval_required_key) {
        lines.push(`latest approval required key: ${snapshot.latest_approval_required_key}`);
      }
      if (snapshot.latest_approval_required_mode) {
        lines.push(`latest approval required mode: ${snapshot.latest_approval_required_mode}`);
      }
    }
    if (snapshot.latest_approval_replay) {
      lines.push(`latest approval replay: ${snapshot.latest_approval_replay}`);
      if (snapshot.latest_approval_replay_source) {
        lines.push(`latest approval replay source: ${snapshot.latest_approval_replay_source}`);
      }
      if (snapshot.latest_approval_replay_key) {
        lines.push(`latest approval replay key: ${snapshot.latest_approval_replay_key}`);
      }
      if (snapshot.latest_approval_replay_transcript) {
        lines.push(`latest approval replay transcript: ${snapshot.latest_approval_replay_transcript}`);
      }
    }
    if (Number.isInteger(snapshot.pending_approval_count) && snapshot.pending_approval_count > 0) {
      lines.push(`pending approvals: ${snapshot.pending_approval_count}`);
    }
    if (Array.isArray(snapshot.pending_approvals) && snapshot.pending_approvals.length > 0) {
      snapshot.pending_approvals.forEach((approval, index) => {
        const header = [approval.tool, approval.operation].filter(Boolean).join("/") || "tool";
        const metadata = [approval.approval_key ? `key=${approval.approval_key}` : "", approval.permission_mode ? `mode=${approval.permission_mode}` : "", approval.source ? `source=${approval.source}` : ""]
          .filter(Boolean)
          .join(" | ");
        lines.push(`pending approval ${index + 1}: ${metadata ? `${header} | ${metadata}` : header}`);
        if (approval.summary) {
          lines.push(`pending approval ${index + 1} summary: ${approval.summary}`);
        }
      });
    }
    if (snapshot.latest_permission_denial) {
      lines.push(`latest permission denial: ${snapshot.latest_permission_denial}`);
      if (snapshot.latest_permission_denial_source) {
        lines.push(`latest permission denial source: ${snapshot.latest_permission_denial_source}`);
      }
    }
    if (Array.isArray(snapshot.remediation_proposals) && snapshot.remediation_proposals.length > 0) {
      lines.push(`remediation proposals: ${snapshot.remediation_proposals.length}`);
      snapshot.remediation_proposals.forEach((proposal, index) => {
        const header = [proposal.proposal_id, proposal.kind].filter(Boolean).join(" | ");
        const summary = proposal.summary || "proposal";
        lines.push(`remediation proposal ${index + 1}: ${header ? `${header} | ${summary}` : summary}`);
        if (proposal.intent) {
          lines.push(`remediation proposal ${index + 1} intent: ${proposal.intent}`);
        }
        if (Array.isArray(proposal.expected_targets) && proposal.expected_targets.length > 0) {
          lines.push(`remediation proposal ${index + 1} targets: ${proposal.expected_targets.length}`);
          proposal.expected_targets.forEach((target) => lines.push(`remediation proposal ${index + 1} target: ${target}`));
        }
        if (proposal.follow_up_command) {
          lines.push(`remediation proposal ${index + 1} follow-up: ${proposal.follow_up_command}`);
        }
      });
    }
    if (Array.isArray(snapshot.suggested_commands) && snapshot.suggested_commands.length > 0) {
      lines.push(`suggested commands: ${snapshot.suggested_commands.length}`);
      snapshot.suggested_commands.forEach((command) => lines.push(`suggested command: ${command}`));
    }
    return lines;
  }
  return [];
}

function renderTaskFeedback(snapshot) {
  const container = document.createElement("div");
  container.className = "task-feedback";

  if (!snapshot) {
    container.textContent = "unavailable";
    return container;
  }

  const scope = document.createElement("div");
  scope.className = "task-feedback-scope";
  scope.textContent = "scope: task";
  container.appendChild(scope);

  const sections = Array.isArray(snapshot.sections) ? snapshot.sections.filter((section) => section?.key) : [];
  if (sections.length === 0) {
    const fallback = document.createElement("div");
    fallback.textContent = "No task feedback sections available.";
    container.appendChild(fallback);
    return container;
  }

  if (!sections.some((section) => section.key === expandedTaskFeedbackSectionKey)) {
    expandedTaskFeedbackSectionKey = sections[0].key;
  }

  const cueRow = document.createElement("div");
  cueRow.className = "task-feedback-section-cues";
  sections.forEach((section) => {
    const cue = document.createElement("button");
    cue.type = "button";
    cue.className = `task-feedback-section-cue ${section.key === expandedTaskFeedbackSectionKey ? "task-feedback-section-cue-active" : ""}`.trim();

    const label = document.createElement("div");
    label.className = "task-feedback-section-cue-label";
    label.textContent = section.label;
    cue.appendChild(label);

    const summary = document.createElement("div");
    summary.className = "task-feedback-section-cue-summary";
    summary.textContent = section.summary || "no summary";
    cue.appendChild(summary);

    cue.addEventListener("click", () => {
      expandedTaskFeedbackSectionKey = section.key;
      taskFeedbackNode.replaceChildren(renderTaskFeedback(latestOperatorSnapshot?.task_feedback));
    });

    cueRow.appendChild(cue);
  });
  container.appendChild(cueRow);

  const activeSection = sections.find((section) => section.key === expandedTaskFeedbackSectionKey) || sections[0];
  if (!activeSection) {
    return container;
  }

  const detail = document.createElement("article");
  detail.className = "task-feedback-section-detail";

  const title = document.createElement("h4");
  title.className = "task-feedback-section-title";
  title.textContent = activeSection.label;
  detail.appendChild(title);

  const meta = document.createElement("div");
  meta.className = "task-feedback-section-meta";
  meta.textContent = activeSection.summary || "no summary";
  detail.appendChild(meta);

  const lines = taskFeedbackSectionLines(snapshot, activeSection.key);
  if (lines.length === 0) {
    const empty = document.createElement("div");
    empty.className = "task-feedback-section-lines";
    empty.textContent = "No detailed lines available.";
    detail.appendChild(empty);
  } else {
    const lineGroup = document.createElement("div");
    lineGroup.className = "task-feedback-section-lines";
    lines.forEach((line) => {
      const row = document.createElement("div");
      row.className = "task-feedback-section-line";
      row.textContent = line;
      lineGroup.appendChild(row);
    });
    detail.appendChild(lineGroup);
  }

  container.appendChild(detail);
  return container;
}

function renderSkillGovernance(snapshot) {
  if (!snapshot) {
    return "unavailable";
  }
  const formatGovernanceAlert = (alert, includeSkillName = false) => {
    const parts = [];
    if (includeSkillName) {
      parts.push(alert.skill_name);
    }
    parts.push(alert.severity, alert.reason);
    if (typeof alert.repairable === "boolean") {
      parts.push(`repairable=${alert.repairable ? "yes" : "no"}`);
    }
    if (alert.blocked_by) {
      parts.push(`blocked-by=${alert.blocked_by}`);
    }
    if (alert.follow_up_command) {
      parts.push(`command: ${alert.follow_up_command}`);
    }
    return parts.join(" | ");
  };
  const lines = [
    `scope: ${snapshot.scope}`,
    `tracked skills: ${snapshot.tracked_skills}`,
    `generated/approved: ${snapshot.generated_count}/${snapshot.approved_count}`,
    `archived/disabled: ${snapshot.archived_count}/${snapshot.disabled_count}`,
    `alerts: ${snapshot.alert_count} (errors ${snapshot.error_count}, warnings ${snapshot.warning_count})`,
  ];
  if (snapshot.overview_command) {
    lines.push(`overview command: ${snapshot.overview_command}`);
  }
  if (snapshot.top_alert) {
    lines.push(`top alert: ${snapshot.top_alert}`);
    if (snapshot.top_alert_command) {
      lines.push(`top alert command: ${snapshot.top_alert_command}`);
    }
  }
  if (snapshot.top_hotspot) {
    lines.push(`top hotspot: ${snapshot.top_hotspot}`);
    if (snapshot.top_hotspot_command) {
      lines.push(`top hotspot command: ${snapshot.top_hotspot_command}`);
    }
  }
  if (snapshot.blocked_invariant_count) {
    lines.push(`blocked invariants: ${snapshot.blocked_invariant_count}`);
    if (snapshot.top_blocked_invariant) {
      lines.push(`top blocked invariant: ${snapshot.top_blocked_invariant}`);
    }
    if (snapshot.top_blocked_invariant_command) {
      lines.push(`top blocked invariant command: ${snapshot.top_blocked_invariant_command}`);
    }
  }
  if (snapshot.current_gap_count) {
    lines.push(`current gaps: ${snapshot.current_gap_count} (missing ${snapshot.missing_current_count || 0}, untracked ${snapshot.untracked_current_count || 0})`);
    if (snapshot.top_current_gap) {
      lines.push(`top current gap: ${snapshot.top_current_gap}`);
    }
    if (snapshot.top_current_gap_command) {
      lines.push(`top current gap command: ${snapshot.top_current_gap_command}`);
    }
  }
  if (snapshot.drift_hotspot_count) {
    lines.push(`drift hotspots: ${snapshot.drift_hotspot_count}`);
    if (snapshot.top_drift_hotspot) {
      lines.push(`top drift hotspot: ${snapshot.top_drift_hotspot}`);
    }
    if (snapshot.top_drift_hotspot_command) {
      lines.push(`top drift hotspot command: ${snapshot.top_drift_hotspot_command}`);
    }
  }
  const selectionItems = Array.isArray(snapshot.selection_items) ? snapshot.selection_items : [];
  const selectionSections = Array.isArray(snapshot.selection_sections) ? snapshot.selection_sections : [];
  const selectedItemKey = snapshot.selected_item_key || "";
  const selectedItem = selectedItemKey ? selectionItems.find((item) => item.key === selectedItemKey) : null;
  const selectedSectionKey = selectedItem?.section || "";
  if (selectionSections.length > 0) {
    lines.push(`section summary: ${renderCompactGovernanceSectionSummary(selectionSections, selectionItems, selectedSectionKey, selectedItemKey)}`);
    lines.push(`section next steps: ${renderCompactGovernanceSectionRollup(selectionSections, selectionItems, selectedSectionKey)}`);
  }
  if (snapshot.selected_detail) {
    const detail = snapshot.selected_detail;
    lines.push(`selected detail: ${detail.skill_name} | current=${detail.current_state || "n/a"} | path=${detail.path || "n/a"}`);
    if (detail.follow_up_command) {
      lines.push(`selected detail command: ${detail.follow_up_command}`);
    }
    if (Array.isArray(detail.alerts) && detail.alerts.length > 0) {
      lines.push("selected detail alerts:");
      detail.alerts.forEach((alert, index) => {
        lines.push(`${index + 1}. ${formatGovernanceAlert(alert)}`);
      });
    }
    if (Array.isArray(detail.recent_transitions) && detail.recent_transitions.length > 0) {
      lines.push("selected detail transitions:");
      detail.recent_transitions.forEach((transition, index) => {
        lines.push(`${index + 1}. ${transition.at || "n/a"} | ${transition.action} | ${transition.from_state || "unknown"} -> ${transition.to_state || "unknown"}`);
      });
    }
  }
  if (snapshot.selected_blocked_invariant) {
    const entry = snapshot.selected_blocked_invariant;
    lines.push(`selected blocked invariant: ${entry.skill_name} | current=${entry.current_state || "n/a"} | path=${entry.path || "n/a"}`);
    lines.push(`selected blocked invariant reason: ${entry.reason}`);
    if (entry.blocked_by) {
      lines.push(`selected blocked invariant blocker: ${entry.blocked_by}`);
    }
    if (entry.follow_up_command) {
      lines.push(`selected blocked invariant command: ${entry.follow_up_command}`);
    }
    if (Array.isArray(entry.recent_ledger_entries) && entry.recent_ledger_entries.length > 0) {
      lines.push("selected blocked invariant ledger entries:");
      entry.recent_ledger_entries.forEach((ledgerEntry, index) => {
        lines.push(`${index + 1}. ${ledgerEntry.at || "n/a"} | ${ledgerEntry.action} | ${ledgerEntry.from_state || "unknown"} -> ${ledgerEntry.to_state || "unknown"} | path=${ledgerEntry.path || "n/a"} | task=${ledgerEntry.source_task_id || "n/a"} | run=${ledgerEntry.source_run_id || "n/a"}`);
      });
    }
    if (Array.isArray(entry.recent_transitions) && entry.recent_transitions.length > 0) {
      lines.push("selected blocked invariant transitions:");
      entry.recent_transitions.forEach((transition, index) => {
        lines.push(`${index + 1}. ${transition.at || "n/a"} | ${transition.action} | ${transition.from_state || "unknown"} -> ${transition.to_state || "unknown"}`);
      });
    }
    if (Array.isArray(entry.related_drifts) && entry.related_drifts.length > 0) {
      lines.push("selected blocked invariant related drifts:");
      entry.related_drifts.forEach((drift, index) => {
        lines.push(`${index + 1}. ${drift.category} | ${drift.summary}`);
      });
    }
  }
  if (snapshot.selected_current_gap) {
    const gap = snapshot.selected_current_gap;
    lines.push(`selected current gap: ${gap.category} | ${gap.skill_name} | path=${gap.path || "n/a"}`);
    if (gap.current_state) {
      lines.push(`selected current gap current state: ${gap.current_state}`);
    }
    if (gap.recorded_state || gap.recorded_action) {
      lines.push(`selected current gap recorded: state=${gap.recorded_state || "n/a"} | action=${gap.recorded_action || "n/a"}`);
    }
    if (gap.content_digest) {
      lines.push(`selected current gap digest: ${gap.content_digest}`);
    }
    if (gap.source_task_id || gap.source_run_id) {
      lines.push(`selected current gap provenance: task=${gap.source_task_id || "n/a"} | run=${gap.source_run_id || "n/a"}`);
    }
    if (gap.follow_up_command) {
      lines.push(`selected current gap command: ${gap.follow_up_command}`);
    }
  }
  if (snapshot.selected_drift_hotspot) {
    const hotspot = snapshot.selected_drift_hotspot;
    lines.push(`selected drift hotspot: ${hotspot.skill_name} | current=${hotspot.current_state || "n/a"} | path=${hotspot.path || "n/a"}`);
    if (hotspot.follow_up_command) {
      lines.push(`selected drift hotspot command: ${hotspot.follow_up_command}`);
    }
    if (Array.isArray(hotspot.drifts) && hotspot.drifts.length > 0) {
      lines.push("selected drift hotspot drifts:");
      hotspot.drifts.forEach((drift, index) => {
        lines.push(`${index + 1}. ${drift.category} | ${drift.summary}`);
      });
    }
  }
  if (Array.isArray(snapshot.alerts) && snapshot.alerts.length > 0) {
    lines.push("alerts list:");
    snapshot.alerts.forEach((alert, index) => {
      lines.push(`${index + 1}. ${formatGovernanceAlert(alert, true)}`);
    });
  }
  if (Array.isArray(snapshot.hotspots) && snapshot.hotspots.length > 0) {
    lines.push("hotspots list:");
    snapshot.hotspots.forEach((hotspot, index) => {
      const command = hotspot.follow_up_command ? ` | command: ${hotspot.follow_up_command}` : "";
      lines.push(`${index + 1}. ${hotspot.skill_name} | transitions=${hotspot.transition_count} | current=${hotspot.current_state}${command}`);
    });
  }
  if (Array.isArray(snapshot.blocked_invariants) && snapshot.blocked_invariants.length > 0) {
    lines.push("blocked invariant list:");
    snapshot.blocked_invariants.forEach((entry, index) => {
      const command = entry.follow_up_command ? ` | command: ${entry.follow_up_command}` : "";
      const blocker = entry.blocked_by ? ` | blocked-by=${entry.blocked_by}` : "";
      lines.push(`${index + 1}. ${entry.skill_name} | current=${entry.current_state || "n/a"} | ${entry.reason}${blocker}${command}`);
    });
  }
  if (Array.isArray(snapshot.current_gaps) && snapshot.current_gaps.length > 0) {
    lines.push("current gap list:");
    snapshot.current_gaps.forEach((gap, index) => {
      const state = gap.current_state ? ` | current=${gap.current_state}` : "";
      const recorded = gap.recorded_state ? ` | recorded=${gap.recorded_state}` : "";
      const action = gap.recorded_action ? ` | action=${gap.recorded_action}` : "";
      const command = gap.follow_up_command ? ` | command: ${gap.follow_up_command}` : "";
      lines.push(`${index + 1}. ${gap.category} | ${gap.skill_name} | path=${gap.path || "n/a"}${state}${recorded}${action}${command}`);
    });
  }
  if (Array.isArray(snapshot.drift_hotspots) && snapshot.drift_hotspots.length > 0) {
    lines.push("drift hotspot list:");
    snapshot.drift_hotspots.forEach((hotspot, index) => {
      const command = hotspot.follow_up_command ? ` | command: ${hotspot.follow_up_command}` : "";
      lines.push(`${index + 1}. ${hotspot.skill_name} | current=${hotspot.current_state || "n/a"} | drifts=${Array.isArray(hotspot.drifts) ? hotspot.drifts.length : 0} | path=${hotspot.path || "n/a"}${command}`);
    });
  }
  if (Array.isArray(snapshot.details) && snapshot.details.length > 0) {
    lines.push("detail previews:");
    snapshot.details.forEach((detail, index) => {
      lines.push(`${index + 1}. ${detail.skill_name} | current=${detail.current_state || "n/a"} | path=${detail.path || "n/a"}`);
      if (detail.follow_up_command) {
        lines.push(`detail command: ${detail.follow_up_command}`);
      }
      if (Array.isArray(detail.alerts) && detail.alerts.length > 0) {
        detail.alerts.forEach((alert) => {
          lines.push(`detail alert: ${formatGovernanceAlert(alert)}`);
        });
      }
      if (Array.isArray(detail.recent_transitions) && detail.recent_transitions.length > 0) {
        detail.recent_transitions.forEach((transition) => {
          lines.push(`detail transition: ${transition.at || "n/a"} | ${transition.action} | ${transition.from_state || "unknown"} -> ${transition.to_state || "unknown"}`);
        });
      }
    });
  }
  return lines.join("\n");
}

function renderSkillGovernanceSelector(snapshot) {
  skillGovernanceSelectorNode.innerHTML = "";

  const defaultOption = document.createElement("option");
  defaultOption.value = "";
  defaultOption.textContent = "Top surfaced item";
  skillGovernanceSelectorNode.appendChild(defaultOption);

  const selectionItems = Array.isArray(snapshot?.selection_items) ? snapshot.selection_items : [];
  const selectionSections = Array.isArray(snapshot?.selection_sections) ? snapshot.selection_sections : [];

  if (!snapshot || selectionItems.length === 0) {
    skillGovernanceSelectorNode.disabled = true;
    skillGovernancePrevNode.disabled = true;
    skillGovernanceNextNode.disabled = true;
    skillGovernancePrevSectionNode.disabled = true;
    skillGovernanceNextSectionNode.disabled = true;
    selectedGovernanceItemKey = "";
    skillGovernanceSelectionStatusNode.textContent = "No governance item details available.";
    skillGovernanceSectionSummaryNode.textContent = "No governance sections available.";
    skillGovernanceSectionOverviewNode.textContent = "No governance section overview available.";
    return;
  }

  skillGovernanceSelectorNode.disabled = false;
  if (selectionSections.length > 0) {
    selectionSections.forEach((section) => {
      const group = document.createElement("optgroup");
      group.label = `${section.label} (${section.count})`;
      selectionItems
        .filter((item) => item.section === section.key)
        .forEach((item) => {
          const option = document.createElement("option");
          option.value = item.key || "";
          option.textContent = item.summary || item.path || item.skill_name || item.key || "governance item";
          group.appendChild(option);
        });
      skillGovernanceSelectorNode.appendChild(group);
    });
  } else {
    selectionItems.forEach((item) => {
      const option = document.createElement("option");
      option.value = item.key || "";
      option.textContent = item.summary || item.path || item.skill_name || item.key || "governance item";
      skillGovernanceSelectorNode.appendChild(option);
    });
  }

  const selectedKey = snapshot.selected_item_key || "";
  if (selectedKey) {
    skillGovernanceSelectorNode.value = selectedKey;
    selectedGovernanceItemKey = selectedKey;
    const selectedName = snapshot.selected_detail?.skill_name || snapshot.selected_blocked_invariant?.skill_name || snapshot.selected_current_gap?.skill_name || snapshot.selected_drift_hotspot?.skill_name || selectedKey;
    const selectedKind = snapshot.selected_item_kind || "governance item";
    const selectedIndex = selectionItems.findIndex((item) => item.key === selectedKey);
    const selectedOrdinal = selectedIndex >= 0 ? selectedIndex + 1 : 0;
    const selectedItem = selectedIndex >= 0 ? selectionItems[selectedIndex] : null;
    const selectedSection = selectedItem ? selectionSections.find((section) => section.key === selectedItem.section) : null;
    const selectedSectionIndex = selectedSection ? selectionSections.findIndex((section) => section.key === selectedSection.key) : -1;
    skillGovernancePrevNode.disabled = selectedIndex <= 0;
    skillGovernanceNextNode.disabled = selectedIndex < 0 || selectedIndex >= selectionItems.length - 1;
    skillGovernancePrevSectionNode.disabled = selectedSectionIndex <= 0;
    skillGovernanceNextSectionNode.disabled = selectedSectionIndex < 0 || selectedSectionIndex >= selectionSections.length - 1;
    const sectionLabel = selectedSection?.label ? ` | section ${selectedSection.label}` : "";
    skillGovernanceSelectionStatusNode.textContent = `Pinned ${selectedKind}: ${selectedName} (${selectedOrdinal} of ${selectionItems.length}${sectionLabel})`;
    skillGovernanceSectionSummaryNode.textContent = renderGovernanceSectionSummary(selectionSections, selectionItems, selectedSection?.key || "", selectedKey);
    renderGovernanceSectionOverview(selectionSections, selectionItems, selectedSection?.key || "", selectedKey);
    return;
  }

  skillGovernanceSelectorNode.value = "";
  selectedGovernanceItemKey = "";
  skillGovernancePrevNode.disabled = true;
  skillGovernanceNextNode.disabled = selectionItems.length <= 1;
  skillGovernancePrevSectionNode.disabled = true;
  skillGovernanceNextSectionNode.disabled = selectionSections.length <= 1;
  skillGovernanceSelectionStatusNode.textContent = "Following top surfaced item.";
  skillGovernanceSectionSummaryNode.textContent = renderGovernanceSectionSummary(selectionSections, selectionItems, "", "");
  renderGovernanceSectionOverview(selectionSections, selectionItems, "", "");
}

function findGovernanceSectionRepresentativeItem(sectionKey, items, selectedItemKey) {
  if (!sectionKey || !Array.isArray(items) || items.length === 0) {
    return null;
  }
  if (selectedItemKey) {
    const selectedItem = items.find((item) => item.section === sectionKey && item.key === selectedItemKey);
    if (selectedItem) {
      return selectedItem;
    }
  }
  return items.find((item) => item.section === sectionKey) || null;
}

function governanceFollowUpHint(item) {
  if (!item) {
    return "inspect surfaced item details";
  }
  return item.follow_up_command || `inspect ${item.skill_name || "item"} details`;
}

function governanceItemPriority(item, governanceSnapshot = latestOperatorSnapshot?.skill_governance) {
  if (item?.priority_label) {
    return { score: typeof item.priority_score === "number" ? item.priority_score : 30, label: item.priority_label };
  }
  if (!item || !governanceSnapshot) {
    return { score: 10, label: "inspect" };
  }
  if (item.kind === "blocked-invariant") {
    return { score: 100, label: "urgent" };
  }
  if (item.kind === "current-gap") {
    const gap = Array.isArray(governanceSnapshot.current_gaps)
      ? governanceSnapshot.current_gaps.find((entry) => entry.path === item.path && entry.skill_name === item.skill_name)
      : null;
    if (gap?.category === "missing-current") {
      return { score: 90, label: "urgent" };
    }
    return { score: 75, label: "review" };
  }
  if (item.kind === "drift-hotspot") {
    const hotspot = Array.isArray(governanceSnapshot.drift_hotspots)
      ? governanceSnapshot.drift_hotspots.find((entry) => entry.path === item.path && entry.skill_name === item.skill_name)
      : null;
    const driftCount = Array.isArray(hotspot?.drifts) ? hotspot.drifts.length : 0;
    if (driftCount >= 3) {
      return { score: 80, label: "urgent" };
    }
    return { score: 65, label: "review" };
  }
  if (item.kind === "detail") {
    const detail = Array.isArray(governanceSnapshot.details)
      ? governanceSnapshot.details.find((entry) => entry.path === item.path && entry.skill_name === item.skill_name)
      : null;
    const alerts = Array.isArray(detail?.alerts) ? detail.alerts : [];
    if (alerts.some((alert) => alert.severity === "error")) {
      return { score: 85, label: "urgent" };
    }
    if (alerts.some((alert) => alert.severity === "warning")) {
      return { score: 60, label: "review" };
    }
  }
  return { score: 30, label: "inspect" };
}

function dominantGovernanceSectionFollowUp(sectionKey, items) {
  if (!sectionKey || !Array.isArray(items) || items.length === 0) {
    return { hint: "inspect surfaced item details", support: 0, total: 0 };
  }
  const sectionItems = items.filter((item) => item.section === sectionKey);
  if (sectionItems.length === 0) {
    return { hint: "inspect surfaced item details", support: 0, total: 0 };
  }
  const counts = new Map();
  const order = [];
  sectionItems.forEach((item) => {
    const hint = governanceFollowUpHint(item);
    if (!counts.has(hint)) {
      counts.set(hint, 0);
      order.push(hint);
    }
    counts.set(hint, counts.get(hint) + 1);
  });
  let selectedHint = order[0];
  let selectedCount = counts.get(selectedHint) || 0;
  order.forEach((hint) => {
    const count = counts.get(hint) || 0;
    if (count > selectedCount) {
      selectedHint = hint;
      selectedCount = count;
    }
  });
  return { hint: selectedHint, support: selectedCount, total: sectionItems.length };
}

function dominantGovernanceSectionPriority(sectionKey, items, governanceSnapshot = latestOperatorSnapshot?.skill_governance) {
  if (!sectionKey || !Array.isArray(items) || items.length === 0) {
    return { score: 10, label: "inspect", support: 0, total: 0 };
  }
  const sectionItems = items.filter((item) => item.section === sectionKey);
  if (sectionItems.length === 0) {
    return { score: 10, label: "inspect", support: 0, total: 0 };
  }
  let selected = governanceItemPriority(sectionItems[0], governanceSnapshot);
  sectionItems.slice(1).forEach((item) => {
    const candidate = governanceItemPriority(item, governanceSnapshot);
    if (candidate.score > selected.score) {
      selected = candidate;
    }
  });
  const support = sectionItems.filter((item) => governanceItemPriority(item, governanceSnapshot).score === selected.score).length;
  return { score: selected.score, label: selected.label, support, total: sectionItems.length };
}

function formatGovernanceSectionFollowUpCue(sectionFollowUp) {
  if (!sectionFollowUp || !sectionFollowUp.hint) {
    return "inspect surfaced item details";
  }
  if (sectionFollowUp.total <= 1) {
    return sectionFollowUp.hint;
  }
  return `${sectionFollowUp.hint} (${sectionFollowUp.support}/${sectionFollowUp.total} items)`;
}

function formatGovernanceSectionPriorityCue(sectionPriority) {
  if (!sectionPriority || !sectionPriority.label) {
    return "inspect";
  }
  if (sectionPriority.total <= 1) {
    return sectionPriority.label;
  }
  return `${sectionPriority.label} (${sectionPriority.support}/${sectionPriority.total} items)`;
}

function compactGovernanceFollowUpHint(hint) {
  if (!hint) {
    return "inspect";
  }
  if (hint.startsWith("avatars skills ")) {
    return hint.slice("avatars skills ".length);
  }
  if (hint.startsWith("avatars ")) {
    return hint.slice("avatars ".length);
  }
  return hint;
}

function formatCompactGovernanceSectionCue(sectionFollowUp) {
  if (!sectionFollowUp || !sectionFollowUp.hint) {
    return "inspect";
  }
  const compactHint = compactGovernanceFollowUpHint(sectionFollowUp.hint);
  if (sectionFollowUp.total <= 1) {
    return compactHint;
  }
  return `${compactHint} [${sectionFollowUp.support}/${sectionFollowUp.total}]`;
}

function formatCompactGovernanceSectionPriorityCue(sectionPriority) {
  if (!sectionPriority || !sectionPriority.label) {
    return "inspect";
  }
  if (sectionPriority.total <= 1) {
    return sectionPriority.label;
  }
  return `${sectionPriority.label}[${sectionPriority.support}/${sectionPriority.total}]`;
}

function formatCompactGovernanceCardCue(sectionPriority, sectionFollowUp) {
  return `cue=${formatCompactGovernanceSectionPriorityCue(sectionPriority)}/${formatCompactGovernanceSectionCue(sectionFollowUp)}`;
}

function compactGovernanceSectionLabel(section) {
  if (!section?.label) {
    return "section";
  }
  return section.label.toLowerCase();
}

function renderGovernanceSectionSummary(sections, items, selectedSectionKey, selectedItemKey) {
  if (!Array.isArray(sections) || sections.length === 0) {
    return "No governance sections available.";
  }
  const counts = sections
    .map((section) => {
      const current = section.key === selectedSectionKey ? "*" : "";
      return `${current}${section.label} ${section.count}`;
    })
    .join(" | ");
  if (!selectedSectionKey) {
    return `Sections: ${counts}`;
  }
  const selectedSection = sections.find((section) => section.key === selectedSectionKey);
  if (!selectedSection) {
    return `Sections: ${counts}`;
  }
  const representativeItem = findGovernanceSectionRepresentativeItem(selectedSectionKey, items, selectedItemKey);
  const dominantFollowUp = dominantGovernanceSectionFollowUp(selectedSectionKey, items);
  const dominantPriority = dominantGovernanceSectionPriority(selectedSectionKey, items);
  const ratio = Array.isArray(items) && items.length > 0 ? Math.round((selectedSection.count / items.length) * 100) : 0;
  return `Focused section: ${selectedSection.label} (${selectedSection.count} item${selectedSection.count === 1 ? "" : "s"}, ${ratio}% of bounded view, priority: ${formatGovernanceSectionPriorityCue(dominantPriority)}, representative: ${governanceFollowUpHint(representativeItem)}, section next: ${formatGovernanceSectionFollowUpCue(dominantFollowUp)}) | Sections: ${counts}`;
}

function renderCompactGovernanceSectionSummary(sections, items, selectedSectionKey, selectedItemKey) {
  if (!Array.isArray(sections) || sections.length === 0) {
    return "No governance sections available.";
  }
  const counts = sections
    .map((section) => `${compactGovernanceSectionLabel(section)}=${section.count}`)
    .join(" | ");
  if (!selectedSectionKey) {
    return `counts: ${counts}`;
  }
  const selectedSection = sections.find((section) => section.key === selectedSectionKey);
  if (!selectedSection) {
    return `counts: ${counts}`;
  }
  const representativeItem = findGovernanceSectionRepresentativeItem(selectedSectionKey, items, selectedItemKey);
  const dominantFollowUp = dominantGovernanceSectionFollowUp(selectedSectionKey, items);
  const dominantPriority = dominantGovernanceSectionPriority(selectedSectionKey, items);
  const ratio = Array.isArray(items) && items.length > 0 ? Math.round((selectedSection.count / items.length) * 100) : 0;
  return `focus=${compactGovernanceSectionLabel(selectedSection)} ${ratio}% | prio=${formatCompactGovernanceSectionPriorityCue(dominantPriority)} | repr=${compactGovernanceFollowUpHint(governanceFollowUpHint(representativeItem))} | lead=${formatCompactGovernanceSectionCue(dominantFollowUp)} | counts: ${counts}`;
}

function renderGovernanceSectionRollup(sections, items, selectedSectionKey, selectedItemKey) {
  if (!Array.isArray(sections) || sections.length === 0) {
    return "No governance section next steps available.";
  }
  return sections
    .map((section) => {
      const dominantFollowUp = dominantGovernanceSectionFollowUp(section.key, items);
      const dominantPriority = dominantGovernanceSectionPriority(section.key, items);
      const current = section.key === selectedSectionKey ? "*" : "";
      return `${current}${section.label} [${formatGovernanceSectionPriorityCue(dominantPriority)}] -> ${formatGovernanceSectionFollowUpCue(dominantFollowUp)}`;
    })
    .join(" | ");
}

function renderCompactGovernanceSectionRollup(sections, items, selectedSectionKey) {
  if (!Array.isArray(sections) || sections.length === 0) {
    return "No governance section next steps available.";
  }
  return sections
    .map((section) => {
      const dominantFollowUp = dominantGovernanceSectionFollowUp(section.key, items);
      const dominantPriority = dominantGovernanceSectionPriority(section.key, items);
      const current = section.key === selectedSectionKey ? "*" : "";
      return `${current}${compactGovernanceSectionLabel(section)}:${formatCompactGovernanceSectionPriorityCue(dominantPriority)}/${formatCompactGovernanceSectionCue(dominantFollowUp)}`;
    })
    .join(" | ");
}

function renderGovernanceSectionOverview(sections, items, selectedSectionKey, selectedItemKey) {
  skillGovernanceSectionOverviewNode.innerHTML = "";
  if (!Array.isArray(sections) || sections.length === 0) {
    skillGovernanceSectionOverviewNode.textContent = "No governance section overview available.";
    return;
  }
  sections.forEach((section) => {
    const card = document.createElement("button");
    card.type = "button";
    card.className = `governance-section-card ${section.key === selectedSectionKey ? "governance-section-card-active" : ""}`.trim();

    const title = document.createElement("div");
    title.className = "governance-section-card-title";

    const titleLabel = document.createElement("span");
    titleLabel.textContent = section.label;
    title.appendChild(titleLabel);

    const badge = document.createElement("span");
    badge.className = "governance-section-card-badge";
    badge.textContent = section.key === selectedSectionKey ? "active" : "section";
    title.appendChild(badge);

    card.appendChild(title);

    const meta = document.createElement("div");
    meta.className = "governance-section-card-meta";
    const ratio = Array.isArray(items) && items.length > 0 ? Math.round((section.count / items.length) * 100) : 0;
    meta.textContent = `${section.count} surfaced item${section.count === 1 ? "" : "s"} | ${ratio}% of bounded view`;
    card.appendChild(meta);

    const representativeItem = findGovernanceSectionRepresentativeItem(section.key, items, selectedItemKey);
    const dominantFollowUp = dominantGovernanceSectionFollowUp(section.key, items);
    const dominantPriority = dominantGovernanceSectionPriority(section.key, items);
    const summary = document.createElement("div");
    summary.className = "governance-section-card-summary";
    if (representativeItem && representativeItem.key === selectedItemKey && section.key === selectedSectionKey) {
      summary.textContent = `Focused: ${representativeItem.summary || representativeItem.skill_name || representativeItem.path || representativeItem.key}`;
    } else if (representativeItem) {
      summary.textContent = `First: ${representativeItem.summary || representativeItem.skill_name || representativeItem.path || representativeItem.key}`;
    } else {
      summary.textContent = "No surfaced item summary.";
    }
    card.appendChild(summary);

    const command = document.createElement("div");
    command.className = "governance-section-card-command";
    command.textContent = formatCompactGovernanceCardCue(dominantPriority, dominantFollowUp);
    card.appendChild(command);

    if (representativeItem?.key) {
      card.addEventListener("click", () => {
        selectedGovernanceItemKey = representativeItem.key;
        reloadStage().catch((error) => {
          statusNode.textContent = `error: ${error.message}`;
        });
      });
    } else {
      card.disabled = true;
    }

    skillGovernanceSectionOverviewNode.appendChild(card);
  });
}

function moveGovernanceSelection(offset) {
  const selectionItems = Array.isArray(latestOperatorSnapshot?.skill_governance?.selection_items) ? latestOperatorSnapshot.skill_governance.selection_items : [];
  const selectedKey = latestOperatorSnapshot?.skill_governance?.selected_item_key || "";
  if (selectionItems.length === 0 || !selectedKey) {
    return;
  }
  const currentIndex = selectionItems.findIndex((item) => item.key === selectedKey);
  if (currentIndex < 0) {
    return;
  }
  const nextIndex = currentIndex + offset;
  if (nextIndex < 0 || nextIndex >= selectionItems.length) {
    return;
  }
  selectedGovernanceItemKey = selectionItems[nextIndex].key;
  reloadStage().catch((error) => {
    statusNode.textContent = `error: ${error.message}`;
  });
}

function moveGovernanceSection(offset) {
  const governanceSnapshot = latestOperatorSnapshot?.skill_governance;
  const selectionItems = Array.isArray(governanceSnapshot?.selection_items) ? governanceSnapshot.selection_items : [];
  const selectionSections = Array.isArray(governanceSnapshot?.selection_sections) ? governanceSnapshot.selection_sections : [];
  const selectedKey = governanceSnapshot?.selected_item_key || "";
  if (selectionItems.length === 0 || selectionSections.length === 0 || !selectedKey) {
    return;
  }
  const selectedItem = selectionItems.find((item) => item.key === selectedKey);
  if (!selectedItem) {
    return;
  }
  const currentSectionIndex = selectionSections.findIndex((section) => section.key === selectedItem.section);
  if (currentSectionIndex < 0) {
    return;
  }
  const nextSectionIndex = currentSectionIndex + offset;
  if (nextSectionIndex < 0 || nextSectionIndex >= selectionSections.length) {
    return;
  }
  const nextSectionKey = selectionSections[nextSectionIndex].key;
  const nextItem = selectionItems.find((item) => item.section === nextSectionKey);
  if (!nextItem) {
    return;
  }
  selectedGovernanceItemKey = nextItem.key;
  reloadStage().catch((error) => {
    statusNode.textContent = `error: ${error.message}`;
  });
}

function valueOrUnavailable(value) {
  return value || "n/a";
}

function renderDiffExplorer(diff) {
  llmDiffNode.textContent = diff?.lines?.join("\n") || "unavailable";
  llmDiffNode.dataset.state = diff?.has_diff ? "changed" : "aligned";
  llmDiffSummaryNode.textContent = diff ? `${diff.changed_count} changed of ${diff.total_count} compared fields` : "unavailable";
  resetDiffFields();

  if (!diff) {
    return;
  }

  const sourceFields = Array.isArray(diff.fields) ? diff.fields : [];
  const visibleFields = diffFilter === "all" ? sourceFields : sourceFields.filter((field) => field.changed);

  if (visibleFields.length === 0) {
    const emptyNode = document.createElement("div");
    emptyNode.className = "diff-empty";
    emptyNode.textContent = diffFilter === "all" ? "No comparable fields for this run view." : "No changed fields for this run view.";
    llmDiffFieldsNode.appendChild(emptyNode);
    return;
  }

  visibleFields.forEach((field) => {
    const card = document.createElement("article");
    card.className = `diff-field ${field.changed ? "diff-field-changed" : "diff-field-aligned"}`;

    const title = document.createElement("h4");
    title.textContent = field.label;
    card.appendChild(title);

    const configured = document.createElement("div");
    configured.className = "diff-field-value";
    configured.textContent = `configured: ${valueOrUnavailable(field.configured)}`;
    card.appendChild(configured);

    const viewedRun = document.createElement("div");
    viewedRun.className = "diff-field-value";
    viewedRun.textContent = `viewed run: ${valueOrUnavailable(field.viewed_run)}`;
    card.appendChild(viewedRun);

    const state = document.createElement("div");
    state.className = "diff-field-state";
    state.textContent = field.changed ? "changed" : "aligned";
    card.appendChild(state);

    llmDiffFieldsNode.appendChild(card);
  });
}

function shouldRefreshOperator(event) {
  if (!["run.started", "run.completed", "llm.status_updated", "telemetry.updated"].includes(event.type)) {
    return false;
  }
  if (selectionMode === "latest") {
    return true;
  }
  const runId = currentHistoryRunId();
  return Boolean(runId) && event.run_id === runId;
}

function updateOperator(snapshot) {
  latestOperatorSnapshot = snapshot;
  renderRunSelector(snapshot.available_runs || [], snapshot.selected_run_id || "");
  renderRunTimeline(snapshot.available_runs || []);
  configuredLLMNode.textContent = renderLLMStatus(snapshot.configured_llm, snapshot.configured_llm_warning);
  latestRunLLMNode.textContent = renderLLMStatus(snapshot.latest_run_llm, snapshot.latest_run_llm_warning);
  latestTelemetryNode.textContent = renderTelemetry(snapshot.latest_telemetry);
  taskFeedbackNode.replaceChildren(renderTaskFeedback(snapshot.task_feedback));
  renderSkillGovernanceSelector(snapshot.skill_governance);
  skillGovernanceNode.textContent = renderSkillGovernance(snapshot.skill_governance);
  renderDiffExplorer(snapshot.llm_diff);
  summaryNode.textContent = snapshot.latest_summary || "No run summary yet.";
}

function addEvent(event) {
  if (!isVisibleEvent(event)) {
    return;
  }

  const item = document.createElement("li");
  item.textContent = `${event.sequence}. ${event.type}`;
  eventLogNode.prepend(item);

  if (event.avatar_id) {
    avatarLabelNode.textContent = event.avatar_id.replace("avatar-", "");
  }

  if (event.type === "avatar.spoke" && event.payload?.content) {
    summaryNode.textContent = event.payload.content;
  }

  if (event.type === "tool.completed" && event.payload?.summary) {
    workflowLabelNode.textContent = event.payload.summary;
    avatarCircleNode.classList.add("avatar-active");
  }

  if (event.type === "run.completed" && event.payload?.summary) {
    summaryNode.textContent = event.payload.summary;
    workflowLabelNode.textContent = "run completed";
  }

  if (event.type === "llm.status_updated") {
    latestRunLLMNode.textContent = renderLLMStatus({
      name: event.payload?.provider,
      family: event.payload?.family,
      model: event.payload?.model,
      think_mode: event.payload?.think_mode,
      supports_web_search: Boolean(event.payload?.supports_web_search),
      web_search_enabled: Boolean(event.payload?.web_search_enabled),
      local: Boolean(event.payload?.local),
      base_url: event.payload?.base_url,
      api_key_env: event.payload?.api_key_env,
    }, event.payload?.warning || "");
  }

  if (event.type === "telemetry.updated") {
    latestTelemetryNode.textContent = renderTelemetry(event.payload);
  }
}

async function loadOperator() {
  const response = await fetch(buildOperatorPath());
  const snapshot = await response.json();
  updateOperator(snapshot);
}

async function loadHistory() {
  resetEventLog();
  const response = await fetch(buildHistoryPath());
  const history = await response.json();
  history.forEach(addEvent);
}

async function reloadStage() {
  await loadOperator();
  await loadHistory();
}

runSelectorNode.addEventListener("change", async () => {
  if (runSelectorNode.value) {
    selectionMode = "fixed";
    pinnedRunId = runSelectorNode.value;
  } else {
    selectionMode = "latest";
    pinnedRunId = "";
  }
  await reloadStage();
});

skillGovernanceSelectorNode.addEventListener("change", async () => {
  selectedGovernanceItemKey = skillGovernanceSelectorNode.value;
  await reloadStage();
});

skillGovernancePrevNode.addEventListener("click", () => moveGovernanceSelection(-1));
skillGovernanceNextNode.addEventListener("click", () => moveGovernanceSelection(1));
skillGovernancePrevSectionNode.addEventListener("click", () => moveGovernanceSection(-1));
skillGovernanceNextSectionNode.addEventListener("click", () => moveGovernanceSection(1));

llmDiffFilterNode.addEventListener("change", () => {
  diffFilter = llmDiffFilterNode.value;
  renderDiffExplorer(latestOperatorSnapshot?.llm_diff);
});

function connectStream() {
  const stream = new EventSource("/api/events/stream");
  stream.onopen = () => {
    statusNode.textContent = "live";
  };
  stream.onmessage = async (message) => {
    const event = JSON.parse(message.data);

    if (shouldRefreshOperator(event)) {
      await reloadStage();
      return;
    }

    addEvent(event);
  };
  stream.onerror = () => {
    statusNode.textContent = "reconnecting";
  };
}

reloadStage().then(() => connectStream()).catch((error) => {
  statusNode.textContent = `error: ${error.message}`;
});
