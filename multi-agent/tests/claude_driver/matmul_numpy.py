import hashlib
import numpy as np

N = 8
A = np.array([[(i * 7 + j * 3 + 1) % 11 for j in range(N)] for i in range(N)])
B = np.array([[(i * 5 + j * 2 + 3) % 13 for j in range(N)] for i in range(N)])
C = (A @ B).tolist()

print("VARIANT: numpy")
print("RESULT_START")
for row in C:
    print(row)
print("RESULT_END")
print("HASH:", hashlib.sha256(str(C).encode()).hexdigest())
