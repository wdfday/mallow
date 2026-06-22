import subprocess
import sys
import os
import glob
import time

def main():
    start_time = time.time()
    
    print("=== Step 1: Generating Comparison Notebooks ===")
    try:
        subprocess.run([sys.executable, "_generate_cmp.py"], check=True)
    except subprocess.CalledProcessError as e:
        print(f"❌ Error generating notebooks: {e}")
        sys.exit(1)
        
    print("\n=== Step 2: Running Execution Pool (Parallel) ===")
    try:
        subprocess.run([sys.executable, "_run_pool.py"], check=True)
    except subprocess.CalledProcessError as e:
        print(f"❌ Error running notebook pool: {e}")
        cleanup()
        sys.exit(1)
        
    print("\n=== Step 3: Extracting Results ===")
    try:
        subprocess.run([sys.executable, "_extract_results.py"], check=True)
    except subprocess.CalledProcessError as e:
        print(f"❌ Error extracting results: {e}")
        cleanup()
        sys.exit(1)
        
    print("\n=== Step 4: Cleaning up Generated Notebooks ===")
    cleanup()
    
    duration = time.time() - start_time
    print(f"\n✅ Pipeline completed successfully in {duration/60:.2f} minutes!")

def cleanup():
    notebooks = glob.glob("cmp_*.ipynb")
    print(f"Removing {len(notebooks)} generated notebooks...")
    for nb in notebooks:
        try:
            os.remove(nb)
        except OSError as e:
            print(f"Error removing {nb}: {e}")
            
if __name__ == "__main__":
    main()
