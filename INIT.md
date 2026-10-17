# Init Prompt

## Purpose

I would like a TUI that shows me the overall status of my tasks based on data from GitHub and JIRA

## Functionality

- I (human) or an AI agent should be able to add, update, and remove a task on the list even if the app is not open.
- Each task should get a line with the following indicators backed by data from the accompanying source
  - JIRA ticket status
  - Github PR approval status
  - Github PR CI status
  - Github PR branch status (can merge / conflict / etc...
  - "All good" indicator based on:
    - Ticket status = QA Approved (maybe this could be configurable?)
    - Github PR is mergeable based on all above being "green"
- App should poll Github and JIRA at regular times (configurable?). This can rely on existing CLIs on the system (gh, acli) or can be baked in, whichever is easier. Needs to be able to authenticate with these services

## Implementation

- You choose the stack but the implementation should be lightweight and fast
- It should also allow for future expansion like possibly panes that show recent Github/JIRA comments (out of scope for MVP but needs to be considered)
- The contents should be viewable on smaller terminal panes, think tmux window splits
