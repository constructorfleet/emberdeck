cmake_minimum_required(VERSION 3.16)
project(emberdeck_esp32_worker_contract C)

enable_testing()

add_library(emberdeck_job_core STATIC
    component/job_core.c
    component/worker_cycle.c
    component/sha256.c)
target_include_directories(emberdeck_job_core PUBLIC component/include)
target_compile_features(emberdeck_job_core PUBLIC c_std_11)
if(CMAKE_C_COMPILER_ID MATCHES "GNU|Clang")
    target_compile_options(emberdeck_job_core PRIVATE -Wall -Wextra -Werror -pedantic)
endif()

add_executable(job_core_test tests/job_core_test.c)
target_link_libraries(job_core_test PRIVATE emberdeck_job_core)
if(CMAKE_C_COMPILER_ID MATCHES "GNU|Clang")
    target_compile_options(job_core_test PRIVATE -Wall -Wextra -Werror -pedantic)
endif()
add_test(NAME job_core_test COMMAND job_core_test)

add_executable(worker_cycle_test tests/worker_cycle_test.c)
target_link_libraries(worker_cycle_test PRIVATE emberdeck_job_core)
if(CMAKE_C_COMPILER_ID MATCHES "GNU|Clang")
    target_compile_options(worker_cycle_test PRIVATE -Wall -Wextra -Werror -pedantic)
endif()
add_test(NAME worker_cycle_test COMMAND worker_cycle_test)
