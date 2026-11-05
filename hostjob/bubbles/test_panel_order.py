import unittest
from panel_order import order, order_bubbles, ordered_panels


class PanelOrderTest(unittest.TestCase):
    def test_grid_reads_rows_left_to_right(self):
        self.assertEqual(order([(0, 0, 10, 10), (12, 0, 22, 10), (0, 12, 10, 22), (12, 12, 22, 22)]), [0, 1, 2, 3])

    def test_tall_left_panel_before_the_stack_beside_it(self):
        self.assertEqual(order([(12, 16, 22, 30), (0, 0, 10, 30), (12, 0, 22, 14)]), [1, 2, 0])

    def test_side_by_side_left_first_even_when_the_right_sits_higher(self):
        self.assertEqual(order([(12, 0, 22, 10), (0, 2, 10, 12)]), [1, 0])

    def test_staircase_balloons_read_left_first(self):
        # in one panel, a balloon on the left that dips just below one on the right reads first
        # (hand-labelled key 2026-10-12: 99 such pairs on 37 pages)
        panels = [[0, 0, 100, 100, 0.9]]
        bubbles = [[60, 10, 30, 20], [10, 26, 30, 20]]  # right one higher, overlapping in y by only 4 px
        self.assertEqual(order_bubbles(100, 100, bubbles, panels), [1, 0])

    def test_staircase_keeps_a_clearly_lower_left_balloon_after(self):
        panels = [[0, 0, 100, 100, 0.9]]
        bubbles = [[60, 10, 30, 20], [10, 60, 30, 20]]  # left one a full balloon lower
        self.assertEqual(order_bubbles(100, 100, bubbles, panels), [0, 1])

    def test_panels_themselves_keep_upper_first(self):
        # the staircase rule is for balloons; a higher right panel still reads before a lower left one
        self.assertEqual(order([(52, 0, 100, 40), (0, 35, 48, 100)]), [0, 1])

    def test_balloons_follow_their_panels(self):
        # tall left panel, two stacked on the right; a balloon in each, given bottom-right first
        panels = [[0, 0, 48, 150, 0.9], [52, 0, 100, 70, 0.9], [52, 80, 100, 150, 0.9]]
        bubbles = [[60, 100, 20, 10], [60, 10, 20, 10], [10, 120, 20, 10]]
        self.assertEqual(order_bubbles(100, 150, bubbles, panels), [2, 1, 0])

    def test_spread_reads_the_left_page_first(self):
        panels = [[0, 50, 95, 100, 0.9], [105, 0, 200, 100, 0.9]]
        bubbles = [[120, 10, 10, 10], [10, 60, 10, 10]]
        self.assertEqual(order_bubbles(200, 100, bubbles, panels), [1, 0])

    def test_inset_reads_with_its_host_and_weak_panels_are_ignored(self):
        panels = [[0, 0, 100, 150, 0.9], [60, 10, 95, 40, 0.9], [0, 0, 100, 20, 0.3]]
        bubbles = [[70, 15, 10, 10], [10, 5, 10, 10]]
        self.assertEqual(order_bubbles(100, 150, bubbles, panels), [1, 0])

    def test_staggered_row_reads_left_to_right(self):
        # 0999 (2026-10-12 audit): "Thanks" sits low between two balloons of a row, which made the
        # pairwise rule loop; break it at the leftmost balloon, not the topmost
        boxes = [(767, 129, 1025, 276), (1441, 109, 1653, 230), (1009, 242, 1179, 347), (1149, 125, 1407, 276)]
        self.assertEqual(order(boxes), [0, 2, 3, 1])

    def test_a_looping_row_starts_at_its_left_end(self):
        # 0268: a row of five staggered balloons loops; it reads left to right, then the balloon below
        boxes = [(1242, 1502, 1399, 1613), (852, 1544, 1102, 1671), (703, 1502, 887, 1587),
                 (799, 2222, 1051, 2387), (1325, 1582, 1599, 1721), (1111, 1566, 1262, 1649)]
        self.assertEqual(order(boxes), [2, 1, 5, 0, 4, 3])

    def test_no_panels_keeps_the_given_order(self):
        self.assertEqual(order_bubbles(100, 150, [[60, 10, 5, 5], [10, 10, 5, 5]], []), [0, 1])


    def test_ordered_panels_drops_low_confidence_and_insets_and_orders(self):
        panels = [[52, 0, 100, 150, 0.9],   # right column
                  [0, 0, 48, 150, 0.9],     # left column, reads first
                  [5, 5, 20, 20, 0.9],      # inset inside the left column
                  [0, 0, 100, 150, 0.3]]    # below MIN_CONF
        self.assertEqual(ordered_panels(100, 150, panels), [[0, 0, 48, 150], [52, 0, 100, 150]])

    def test_unfolded_keeps_the_inset(self):
        panels = [[0, 0, 48, 150, 0.9], [5, 5, 20, 20, 0.9]]
        self.assertEqual(ordered_panels(100, 150, panels, fold=False), [[0, 0, 48, 150], [5, 5, 20, 20]])

    def test_ordered_panels_with_nothing_usable_is_empty(self):
        self.assertEqual(ordered_panels(100, 150, [[0, 0, 100, 150, 0.3]]), [])

    def test_the_cutoff_is_the_callers(self):
        panels = [[0, 0, 48, 150, 0.4], [52, 0, 100, 150, 0.9]]
        self.assertEqual(ordered_panels(100, 150, panels), [[52, 0, 100, 150]])
        self.assertEqual(ordered_panels(100, 150, panels, min_conf=0.3), [[0, 0, 48, 150], [52, 0, 100, 150]])

    def test_order_bubbles_passes_the_cutoff_through(self):
        panels = [[0, 0, 48, 150, 0.4], [52, 0, 100, 150, 0.4]]
        bubbles = [[60, 10, 20, 10], [10, 10, 20, 10]]
        self.assertEqual(order_bubbles(100, 150, bubbles, panels), [0, 1])  # no usable panels: as given
        self.assertEqual(order_bubbles(100, 150, bubbles, panels, min_conf=0.3), [1, 0])


if __name__ == "__main__":
    unittest.main()
