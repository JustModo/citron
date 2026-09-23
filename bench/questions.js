/**
 * Problem suite and payload generator for citron load testing.
 */

const LANGUAGES = {
  python: 71,
  cpp: 54,
  java: 62,
  c: 50,
};

const QUESTIONS = [
  // --- Light Tier: O(1) / Basic I/O ---
  {
    id: 'light_addition_python',
    tier: 'light',
    language_id: LANGUAGES.python,
    langName: 'Python 3',
    source: `import sys

def main():
    lines = sys.stdin.read().splitlines()
    for line in lines:
        if not line.strip():
            continue
        parts = line.split()
        if len(parts) >= 2:
            print(int(parts[0]) + int(parts[1]))

if __name__ == "__main__":
    main()
`,
    generateTestcase: (i) => ({
      stdin: `${i * 3} ${i * 7}`,
      expected_output: `${i * 3 + i * 7}`,
    }),
  },
  {
    id: 'light_addition_cpp',
    tier: 'light',
    language_id: LANGUAGES.cpp,
    langName: 'C++',
    source: `#include <iostream>
using namespace std;

int main() {
    long long a, b;
    while (cin >> a >> b) {
        cout << (a + b) << "\\n";
    }
    return 0;
}
`,
    generateTestcase: (i) => ({
      stdin: `${i * 5} ${i * 12}`,
      expected_output: `${i * 5 + i * 12}`,
    }),
  },
  {
    id: 'light_addition_c',
    tier: 'light',
    language_id: LANGUAGES.c,
    langName: 'C',
    source: `#include <stdio.h>

int main() {
    long long a, b;
    while (scanf("%lld %lld", &a, &b) == 2) {
        printf("%lld\\n", a + b);
    }
    return 0;
}
`,
    generateTestcase: (i) => ({
      stdin: `${i * 4} ${i * 9}`,
      expected_output: `${i * 4 + i * 9}`,
    }),
  },

  // --- Medium Tier: O(N log N) / Sorting & Sequence Calculations ---
  {
    id: 'medium_sort_python',
    tier: 'medium',
    language_id: LANGUAGES.python,
    langName: 'Python 3',
    source: `import sys

def main():
    lines = sys.stdin.read().splitlines()
    for line in lines:
        if not line.strip():
            continue
        nums = list(map(int, line.split()))
        nums.sort()
        print(" ".join(map(str, nums)))

if __name__ == "__main__":
    main()
`,
    generateTestcase: (i) => {
      const arr = Array.from({ length: 50 }, (_, k) => (i * 13 + k * 7) % 100);
      const sorted = [...arr].sort((a, b) => a - b);
      return {
        stdin: arr.join(' '),
        expected_output: sorted.join(' '),
      };
    },
  },
  {
    id: 'medium_fibonacci_cpp',
    tier: 'medium',
    language_id: LANGUAGES.cpp,
    langName: 'C++',
    source: `#include <iostream>
using namespace std;

long long fib(int n) {
    if (n <= 1) return n;
    long long a = 0, b = 1, c = 0;
    for (int i = 2; i <= n; ++i) {
        c = a + b;
        a = b;
        b = c;
    }
    return b;
}

int main() {
    int n;
    while (cin >> n) {
        cout << fib(n % 50) << "\\n";
    }
    return 0;
}
`,
    generateTestcase: (i) => {
      const n = (i % 45) + 1;
      let a = 0n, b = 1n, c = 0n;
      for (let k = 2; k <= n; k++) {
        c = a + b;
        a = b;
        b = c;
      }
      return {
        stdin: `${n}`,
        expected_output: `${n <= 1 ? n : b.toString()}`,
      };
    },
  },

  // --- Heavy Tier: CPU Intensive / Nested Loops & Primes ---
  {
    id: 'heavy_primes_python',
    tier: 'heavy',
    language_id: LANGUAGES.python,
    langName: 'Python 3',
    source: `import sys

def count_primes(limit):
    if limit < 2:
        return 0
    is_prime = [True] * (limit + 1)
    is_prime[0] = is_prime[1] = False
    for p in range(2, int(limit**0.5) + 1):
        if is_prime[p]:
            for i in range(p*p, limit + 1, p):
                is_prime[i] = False
    return sum(is_prime)

def main():
    lines = sys.stdin.read().splitlines()
    for line in lines:
        if not line.strip():
            continue
        limit = int(line.strip())
        print(count_primes(limit))

if __name__ == "__main__":
    main()
`,
    generateTestcase: (i) => {
      const limit = 5000 + (i % 10) * 1000;
      const countPrimes = (lim) => {
        let isPrime = new Uint8Array(lim + 1).fill(1);
        isPrime[0] = isPrime[1] = 0;
        for (let p = 2; p * p <= lim; p++) {
          if (isPrime[p]) {
            for (let k = p * p; k <= lim; k += p) isPrime[k] = 0;
          }
        }
        return isPrime.reduce((acc, val) => acc + val, 0);
      };
      return {
        stdin: `${limit}`,
        expected_output: `${countPrimes(limit)}`,
      };
    },
  },
  {
    id: 'heavy_matrix_cpp',
    tier: 'heavy',
    language_id: LANGUAGES.cpp,
    langName: 'C++',
    source: `#include <iostream>
#include <vector>
using namespace std;

int main() {
    int n;
    while (cin >> n) {
        if (n > 80) n = 80;
        long long sum = 0;
        for (int i = 0; i < n; ++i) {
            for (int j = 0; j < n; ++j) {
                sum += (i * j);
            }
        }
        cout << sum << "\\n";
    }
    return 0;
}
`,
    generateTestcase: (i) => {
      const n = (i % 30) + 20;
      let sum = 0n;
      for (let r = 0; r < n; r++) {
        for (let c = 0; c < n; c++) {
          sum += BigInt(r * c);
        }
      }
      return {
        stdin: `${n}`,
        expected_output: `${sum.toString()}`,
      };
    },
  },
];

/**
 * Builds a submission request payload formatted for POST /submissions.
 * @param {string} [tier='all'] - 'light', 'medium', 'heavy', or 'all'
 * @param {number} [testcaseCount=100] - Testcase batch size
 * @param {number} [userIndex=1] - Virtual user index
 * @param {number} [subIndex=1] - Submission sequence index
 * @returns {Object} JSON payload for /submissions API
 */
function getSubmissionPayload(tier = 'all', testcaseCount = 100, userIndex = 1, subIndex = 1) {
  let pool = QUESTIONS;
  if (tier && tier !== 'all') {
    pool = QUESTIONS.filter((q) => q.tier === tier);
    if (pool.length === 0) pool = QUESTIONS;
  }

  const selected = pool[(userIndex + subIndex) % pool.length];
  const testcases = [];
  for (let i = 1; i <= testcaseCount; i++) {
    testcases.push(selected.generateTestcase(i));
  }

  let sourceCode = selected.source;
  if (selected.language_id === LANGUAGES.python) {
    sourceCode += `\n# user: ${userIndex}, submission: ${subIndex}\n`;
  } else if (selected.language_id === LANGUAGES.cpp || selected.language_id === LANGUAGES.c) {
    sourceCode += `\n// user: ${userIndex}, submission: ${subIndex}\n`;
  }

  return {
    language_id: selected.language_id,
    source_code: sourceCode,
    testcases,
    metadata: {
      questionId: selected.id,
      tier: selected.tier,
      langName: selected.langName,
    },
  };
}

module.exports = {
  LANGUAGES,
  QUESTIONS,
  getSubmissionPayload,
};
