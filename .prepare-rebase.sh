#!/bin/bash
TODO_FILE="$1"

COMMITS_TO_EDIT="092f6f1
3a6fe24
478d97c
639b42b
7fc3856
8060d1a
e65241e
efae41b"

for commit in $COMMITS_TO_EDIT; do
  sed -i "s/^pick \(${commit}\)/edit \1/" "$TODO_FILE"
done

echo "Rebase todo modified. Commits set to 'edit':"
grep "^edit" "$TODO_FILE" | head -10
