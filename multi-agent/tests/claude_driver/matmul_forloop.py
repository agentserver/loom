import hashlib

N = 8
A = [[(i * 7 + j * 3 + 1) % 11 for j in range(N)] for i in range(N)]
B = [[(i * 5 + j * 2 + 3) % 13 for j in range(N)] for i in range(N)]
C = [[0] * N for _ in range(N)]
for i in range(N):
    for j in range(N):
        s = 0
        for k in range(N):
            s += A[i][k] * B[k][j]
        C[i][j] = s

print("VARIANT: forloop")
print("RESULT_START")
for row in C:
    print(row)
print("RESULT_END")
print("HASH:", hashlib.sha256(str(C).encode()).hexdigest())
