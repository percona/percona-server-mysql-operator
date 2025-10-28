#!/usr/bin/env bash

comment_fields() {
	local tmp_old tmp_new tmp_diff tmp_out

	tmp_old=$(mktemp)
	tmp_new=$(mktemp)
	tmp_diff=$(mktemp)
	tmp_out=$(mktemp)

	yq - "$@" >"$tmp_old"
	del_fields_to_comment <"$tmp_old" >"$tmp_new"

	diff "$tmp_old" "$tmp_new" >"$tmp_diff"

	# convert delete-only hunks into change hunks with identical new-range,
	# and duplicate "< ..." lines as "> # ..."
	awk '
/^[0-9]+(,[0-9]+)?[acd][0-9]+(,[0-9]+)?$/ {
  if (inblock) process()
  hdr = $0; inblock = 1; n = 0
  next
}
inblock { block[++n] = $0; next }
{ print }

END { if (inblock) process() }

function process(i, ln, has_lt, has_gt, has_sep, pos, ch, left) {
  has_lt = has_gt = has_sep = 0
  for (i = 1; i <= n; i++) {
    ln = block[i]
    if (ln ~ /^</) has_lt = 1
    else if (ln ~ /^>/) has_gt = 1
    else if (ln ~ /^---$/) has_sep = 1
  }

  if (has_lt && !has_gt) {
    # if header was a delete, make it a change with identical new-range
    pos = 0
    for (i = 1; i <= length(hdr); i++) { ch = substr(hdr, i, 1); if (ch=="a"||ch=="c"||ch=="d"){ pos = i; break } }
    if (pos && substr(hdr, pos, 1) == "d") {
      left = substr(hdr, 1, pos-1)
      hdr = left "c" left
    } else if (match(hdr, /[acd]/)) {
      pos = RSTART
      if (substr(hdr, pos, 1) != "c") hdr = substr(hdr,1,pos-1) "c" substr(hdr,pos+1)
    }

    print hdr
    for (i = 1; i <= n; i++) print block[i]
    if (!has_sep) print "---"
    for (i = 1; i <= n; i++) {
      ln = block[i]
      if (ln ~ /^</) { sub(/^< ?/, "", ln); print "> #" ln }
    }
  } else {
    print hdr
    for (i = 1; i <= n; i++) print block[i]
  }
  inblock = 0; n = 0
}' "$tmp_diff" >"$tmp_out"

	mv "$tmp_out" "$tmp_diff"

	patch -s -i "$tmp_diff" "$tmp_old"

	cat "$tmp_old"
}
