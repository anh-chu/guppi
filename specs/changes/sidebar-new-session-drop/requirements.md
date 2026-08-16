# Requirements

## New-session drop on a sidebar row

GIVEN a visible sidebar session row and a drag carrying `application/x-termyard-new-session`
WHEN the drag is dropped on that row
THEN the sidebar calls its New-session drop callback with that row's session key and the established `center` placement.

## Existing session reorder behavior

GIVEN a sidebar session reorder drag carrying `text/plain`
WHEN it is dropped on another sidebar row
THEN pairing and above/below ordering behavior remains unchanged.
