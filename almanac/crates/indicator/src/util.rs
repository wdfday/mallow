use std::collections::VecDeque;

/// O(1) amortized rolling maximum trên sliding window.
///
/// Dùng monotonic deque: duy trì deque các cặp `(absolute_index, value)` theo
/// thứ tự **giảm dần** của value. Khi push value mới, pop tất cả phần tử ở
/// tail có value ≤ value mới (chúng không bao giờ có thể là max trong tương lai).
/// Front của deque luôn là max của window hiện tại.
///
/// # Complexity
/// - `push`: O(1) amortized (mỗi element push/pop đúng 1 lần)
/// - `value`: O(1)
/// - Space: O(period)
#[derive(Debug, Clone)]
pub struct RollingMax {
    /// Monotonic deque: (absolute_index, value), giảm dần theo value
    deque: VecDeque<(usize, f64)>,
    period: usize,
    /// Tổng số element đã push (dùng để tính window boundary)
    count: usize,
}

impl RollingMax {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "RollingMax period must be > 0");
        Self {
            deque: VecDeque::new(),
            period,
            count: 0,
        }
    }

    /// Push value mới. Trả về `Some(max)` khi window đủ `period` element.
    pub fn push(&mut self, val: f64) -> Option<f64> {
        // Pop tail nếu nhỏ hơn hoặc bằng val — chúng không thể là max tương lai
        while self.deque.back().map_or(false, |&(_, v)| v <= val) {
            self.deque.pop_back();
        }
        self.deque.push_back((self.count, val));
        self.count += 1;

        // Evict front nếu đã ra ngoài window
        let window_start = self.count.saturating_sub(self.period);
        while self.deque.front().map_or(false, |&(i, _)| i < window_start) {
            self.deque.pop_front();
        }

        if self.count >= self.period {
            self.deque.front().map(|&(_, v)| v)
        } else {
            None
        }
    }

    pub fn value(&self) -> Option<f64> {
        if self.count >= self.period {
            self.deque.front().map(|&(_, v)| v)
        } else {
            None
        }
    }

    pub fn reset(&mut self) {
        self.deque.clear();
        self.count = 0;
    }
}

/// O(1) amortized rolling minimum trên sliding window.
///
/// Tương tự `RollingMax` nhưng duy trì deque theo thứ tự **tăng dần**.
#[derive(Debug, Clone)]
pub struct RollingMin {
    /// Monotonic deque: (absolute_index, value), tăng dần theo value
    deque: VecDeque<(usize, f64)>,
    period: usize,
    count: usize,
}

impl RollingMin {
    pub fn new(period: usize) -> Self {
        assert!(period > 0, "RollingMin period must be > 0");
        Self {
            deque: VecDeque::new(),
            period,
            count: 0,
        }
    }

    /// Push value mới. Trả về `Some(min)` khi window đủ `period` element.
    pub fn push(&mut self, val: f64) -> Option<f64> {
        // Pop tail nếu lớn hơn hoặc bằng val
        while self.deque.back().map_or(false, |&(_, v)| v >= val) {
            self.deque.pop_back();
        }
        self.deque.push_back((self.count, val));
        self.count += 1;

        let window_start = self.count.saturating_sub(self.period);
        while self.deque.front().map_or(false, |&(i, _)| i < window_start) {
            self.deque.pop_front();
        }

        if self.count >= self.period {
            self.deque.front().map(|&(_, v)| v)
        } else {
            None
        }
    }

    pub fn value(&self) -> Option<f64> {
        if self.count >= self.period {
            self.deque.front().map(|&(_, v)| v)
        } else {
            None
        }
    }

    pub fn reset(&mut self) {
        self.deque.clear();
        self.count = 0;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_rolling_max_basic() {
        let mut rm = RollingMax::new(3);
        assert_eq!(rm.push(1.0), None);
        assert_eq!(rm.push(3.0), None);
        assert_eq!(rm.push(2.0), Some(3.0)); // window [1,3,2] → max=3
        assert_eq!(rm.push(5.0), Some(5.0)); // window [3,2,5] → max=5
        assert_eq!(rm.push(4.0), Some(5.0)); // window [2,5,4] → max=5
        assert_eq!(rm.push(1.0), Some(5.0)); // window [5,4,1] → max=5
        assert_eq!(rm.push(1.0), Some(4.0)); // window [4,1,1] → max=4
        assert_eq!(rm.push(1.0), Some(1.0)); // window [1,1,1] → max=1
    }

    #[test]
    fn test_rolling_max_eviction() {
        // Verify max is evicted correctly when it leaves the window
        let mut rm = RollingMax::new(3);
        rm.push(10.0); // idx=0
        rm.push(1.0);  // idx=1
        rm.push(1.0);  // idx=2, window=[10,1,1] → 10
        assert_eq!(rm.value(), Some(10.0));
        rm.push(1.0);  // idx=3, window=[1,1,1] → 1 (10 evicted)
        assert_eq!(rm.value(), Some(1.0));
    }

    #[test]
    fn test_rolling_min_basic() {
        let mut rm = RollingMin::new(3);
        assert_eq!(rm.push(5.0), None);
        assert_eq!(rm.push(3.0), None);
        assert_eq!(rm.push(4.0), Some(3.0)); // [5,3,4] → min=3
        assert_eq!(rm.push(1.0), Some(1.0)); // [3,4,1] → min=1
        assert_eq!(rm.push(2.0), Some(1.0)); // [4,1,2] → min=1
        assert_eq!(rm.push(6.0), Some(1.0)); // [1,2,6] → min=1
    }

    #[test]
    fn test_rolling_min_eviction() {
        let mut rm = RollingMin::new(3);
        rm.push(1.0);  // idx=0
        rm.push(9.0);
        rm.push(9.0);  // window=[1,9,9] → 1
        assert_eq!(rm.value(), Some(1.0));
        rm.push(9.0);  // window=[9,9,9] → 9 (1 evicted)
        assert_eq!(rm.value(), Some(9.0));
    }

    #[test]
    fn test_rolling_max_constant() {
        let mut rm = RollingMax::new(4);
        for _ in 0..10 {
            if let Some(v) = rm.push(42.0) {
                assert_eq!(v, 42.0);
            }
        }
    }

    #[test]
    fn test_rolling_min_constant() {
        let mut rm = RollingMin::new(4);
        for _ in 0..10 {
            if let Some(v) = rm.push(7.0) {
                assert_eq!(v, 7.0);
            }
        }
    }

    #[test]
    fn test_rolling_max_reset() {
        let mut rm = RollingMax::new(3);
        rm.push(1.0);
        rm.push(2.0);
        rm.push(3.0);
        assert!(rm.value().is_some());
        rm.reset();
        assert!(rm.value().is_none());
        assert_eq!(rm.push(5.0), None);
    }
}
