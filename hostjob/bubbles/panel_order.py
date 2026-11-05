"""Panel-first reading order for a page's balloons. Stdlib only, so bubblejob can import it outside the venv.

The Swift tool orders balloons from where they sit; it goes wrong when art crosses gutters or panels are
uneven. With panel boxes from a detector (run_panels.py), balloons are read panel by panel instead: panels
in reading order, each balloon in the panel it overlaps most, balloons within a panel by the same rule.
Spike 2026-10-10 on 98 regression + 24 Legion pages: 18 pages better, 5 worse (all from bad panel boxes).
Boxes are (x0, y0, x1, y1).
"""

MIN_CONF = 0.5
SAME_ROW = 0.25  # boxes overlapping vertically by this much of the shorter one share a row
OWN_PANEL = 0.3  # a balloon less than this much inside every panel goes to the nearest one


def _before(a, b, stair=False):
    """True if a reads before b: left first when side by side, else upper first. Nested or heavily
    overlapping boxes get no call and fall to the tie-break. With stair (balloons inside a panel),
    boxes side by side that overlap vertically at all also read left first: a staircase of balloons
    (hand-labelled key 2026-10-12: 99 pairs on 37 pages read the lower-left one first)."""
    h = max(0, min(a[2], b[2]) - max(a[0], b[0])) / max(1, min(a[2] - a[0], b[2] - b[0]))
    v = max(0, min(a[3], b[3]) - max(a[1], b[1])) / max(1, min(a[3] - a[1], b[3] - b[1]))
    if (v >= SAME_ROW or (stair and v > 0)) and h < SAME_ROW:
        return a[0] + a[2] < b[0] + b[2]
    if v < SAME_ROW:
        return a[1] + a[3] < b[1] + b[3]
    return False


def order(boxes, stair=False):
    """Indices of boxes in reading order: everything a box must follow comes first, ties go top then left.
    ponytail: O(n^2) pairs per page, fine for the ~30 boxes a page has."""
    n = len(boxes)
    after = [{j for j in range(n) if j != i and _before(boxes[j], boxes[i], stair)} for i in range(n)]
    done, out = set(), []
    while len(out) < n:
        left = [i for i in range(n) if i not in done]
        ready = [i for i in left if after[i] <= done]
        if ready:
            i = min(ready, key=lambda i: (boxes[i][1], boxes[i][0]))
        else:  # a cycle (a staggered row): the box waiting on fewest others, then the leftmost
            i = min(left, key=lambda i: (len(after[i] - done), boxes[i][0]))
        done.add(i)
        out.append(i)
    return out


def _inter(a, b):
    return max(0, min(a[2], b[2]) - max(a[0], b[0])) * max(0, min(a[3], b[3]) - max(a[1], b[1]))


def _area(a):
    return (a[2] - a[0]) * (a[3] - a[1])


def ordered_panels(w, h, panels, min_conf=MIN_CONF, fold=True):
    """Usable panels in reading order, each [x0, y0, x1, y1]: confidence >= min_conf (the installed
    model's cutoff, panel.conf), insets folded into their host (fold), a spread read left page then right.
    The sidecar's panel-zoom list is written unfolded: with the round-2 detector at 0.4, folding costs
    7 good pages on the 150-page key (2026-10-29). Balloon order still folds."""
    P = [p[:4] for p in panels if p[4] >= min_conf]
    if fold:  # an inset mostly inside a bigger panel reads with its host
        P = [p for p in P if not any(_area(q) > _area(p) and _inter(p, q) > 0.8 * _area(p) for q in P)]
    if w <= h:
        return [P[i] for i in order(P)]
    # a spread: the left page's panels, then the right page's
    halves = [[p for p in P if (p[0] + p[2] < w) == left] for left in (True, False)]
    return [half[i] for half in halves for i in order(half)]


def order_bubbles(w, h, bubbles, panels, min_conf=MIN_CONF):
    """Reading order (indices into bubbles, each [x, y, w, h]) given panels [[x0, y0, x1, y1, conf]].
    With no usable panels, the order is left as it was."""
    P = ordered_panels(w, h, panels, min_conf)
    B = [(x, y, x + bw, y + bh) for x, y, bw, bh in bubbles]
    if not P:
        return list(range(len(B)))
    groups = [[] for _ in P]
    for i, b in enumerate(B):
        k = max(range(len(P)), key=lambda k: _inter(b, P[k]))
        if _inter(b, P[k]) < OWN_PANEL * max(1, _area(b)):
            cx, cy = b[0] + b[2], b[1] + b[3]
            k = min(range(len(P)), key=lambda k: (P[k][0] + P[k][2] - cx) ** 2 + (P[k][1] + P[k][3] - cy) ** 2)
        groups[k].append(i)
    return [g[i] for g in groups for i in order([B[j] for j in g], stair=True)]
