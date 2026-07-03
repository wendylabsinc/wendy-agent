idf_component_register(SRCS "demo_main.c"
                       INCLUDE_DIRS "."
                       REQUIRES driver esp_lcd esp_psram esp_driver_cam esp_timer
                                espressif__esp32-camera
                                espressif__esp32_s31_korvo_1)

# Optional build knobs, toggled from the command line, e.g.:
#   idf.py --preview build -DDEMO_ENABLE_CAMERA=1
#   idf.py --preview build -DDEMO_ENABLE_WENDY_CORE=1
# Translate the CMake cache vars into C preprocessor defines.
if(DEFINED DEMO_ENABLE_CAMERA)
    target_compile_definitions(${COMPONENT_LIB} PRIVATE DEMO_ENABLE_CAMERA=${DEMO_ENABLE_CAMERA})
endif()

# Phase 2: when the wendy_core dependency is enabled in idf_component.yml, add
# it to REQUIRES above (REQUIRES ... wendy_core) and build with
# -DDEMO_ENABLE_WENDY_CORE=1.
if(DEFINED DEMO_ENABLE_WENDY_CORE)
    target_compile_definitions(${COMPONENT_LIB} PRIVATE DEMO_ENABLE_WENDY_CORE=${DEMO_ENABLE_WENDY_CORE})
endif()
