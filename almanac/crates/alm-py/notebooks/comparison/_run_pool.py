import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from _generate_cmp import STRATEGY_IMPLS

# Cấu hình số lượng worker song song
MAX_WORKERS = 4

# Tên kernel dùng riêng cho pipeline này — KHÔNG dùng tên kernel cố định kiểu
# "alm-py-venv" đã đăng ký thủ công trước đây: kernelspec đó neo cứng đường
# dẫn tuyệt đối tới .venv tại thời điểm cài, nên nếu repo bị di chuyển/nhân
# bản (vd. "mallow copy") kernelspec cũ trỏ sai chỗ và fail âm thầm (nbconvert
# nuốt lỗi qua stdout/stderr=DEVNULL). `ensure_kernel()` đăng ký lại kernel
# này từ `sys.executable` hiện tại mỗi lần chạy pipeline — tự phục hồi, không
# bao giờ trỏ nhầm venv nữa.
KERNEL_NAME = "alm-py-current"

def ensure_kernel() -> None:
    """(Re)đăng ký kernelspec `KERNEL_NAME` trỏ đúng vào `sys.executable` hiện tại."""
    subprocess.run(
        [
            sys.executable, "-m", "ipykernel", "install", "--user",
            "--name", KERNEL_NAME,
            "--display-name", f"alm-py (current venv: {sys.executable})",
        ],
        check=True,
    )

def run_notebook(nb_file: str) -> tuple[str, bool, float]:
    """Chạy một file notebook thông qua jupyter nbconvert trong một subprocess."""
    start_time = time.time()
    try:
        cmd = [
            sys.executable, "-m", "jupyter", "nbconvert",
            "--to", "notebook",
            "--execute",
            f"--ExecutePreprocessor.kernel_name={KERNEL_NAME}",
            "--inplace",
            nb_file
        ]
        subprocess.run(cmd, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        duration = time.time() - start_time
        return nb_file, True, duration
    except Exception as e:
        duration = time.time() - start_time
        return nb_file, False, duration

def main():
    ensure_kernel()

    # Lấy danh sách notebook từ định nghĩa chiến lược trong _generate_cmp.py
    notebooks = [f"cmp_{name}.ipynb" for name in STRATEGY_IMPLS.keys()]
    total_files = len(notebooks)

    print(f"=== Bắt đầu chạy song song {total_files} notebooks với {MAX_WORKERS} workers ===")
    start_all = time.time()
    
    success_count = 0
    failure_count = 0
    failed_list = []

    with ThreadPoolExecutor(max_workers=MAX_WORKERS) as executor:
        futures = {executor.submit(run_notebook, nb): nb for nb in notebooks}
        
        for future in as_completed(futures):
            nb_file, success, duration = future.result()
            if success:
                success_count += 1
                print(f"✅ [OK] {nb_file} - Hoàn thành sau {duration:.2f}s")
            else:
                failure_count += 1
                failed_list.append(nb_file)
                print(f"❌ [LỖI] {nb_file} - Thất bại sau {duration:.2f}s")

    total_duration = time.time() - start_all
    print("\n" + "="*50)
    print("=== TỔNG KẾT QUÁ TRÌNH CHẠY ===")
    print(f"Tổng thời gian thực thi: {total_duration:.2f}s (~ {total_duration/60:.2f} phút)")
    print(f"Thành công: {success_count}/{total_files}")
    print(f"Thất bại: {failure_count}/{total_files}")
    if failed_list:
        print("Danh sách các file bị lỗi:")
        for f in failed_list:
            print(f"  - {f}")
    print("="*50)

if __name__ == "__main__":
    main()
