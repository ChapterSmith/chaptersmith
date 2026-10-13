package audiotest

/*
#cgo windows LDFLAGS: -lpsapi
#include <stdint.h>
#if defined(__APPLE__)
#include <mach/mach.h>
static uint64_t fixture_rss(void){struct mach_task_basic_info info;mach_msg_type_number_t n=MACH_TASK_BASIC_INFO_COUNT;if(task_info(mach_task_self(),MACH_TASK_BASIC_INFO,(task_info_t)&info,&n)!=KERN_SUCCESS)return 0;return info.resident_size;}
#elif defined(_WIN32)
#include <windows.h>
#include <psapi.h>
static uint64_t fixture_rss(void){PROCESS_MEMORY_COUNTERS info;if(!GetProcessMemoryInfo(GetCurrentProcess(),&info,sizeof(info)))return 0;return info.WorkingSetSize;}
#else
#include <stdio.h>
#include <unistd.h>
static uint64_t fixture_rss(void){unsigned long total,resident;FILE *f=fopen("/proc/self/statm","r");if(!f)return 0;int n=fscanf(f,"%lu %lu",&total,&resident);fclose(f);return n==2?(uint64_t)resident*sysconf(_SC_PAGESIZE):0;}
#endif
*/
import "C"

// ResidentBytes includes native decoder/resampler/packet allocations, unlike
// Go's heap metrics. Used only by streaming resource regressions.
func ResidentBytes() uint64 { return uint64(C.fixture_rss()) }
