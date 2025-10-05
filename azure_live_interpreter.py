import azure.cognitiveservices.speech as speechsdk
from azure.cognitiveservices.speech.languageconfig import AutoDetectSourceLanguageConfig

# Please replace the service region with your region
region = "YourRegion"
v2_endpoint_string = f"wss://{region}.stt.speech.microsoft.com/speech/universal/v2"

# Creates an instance of a speech translation config with specified subscription key and service region.
# Please replace the service subscription key with your subscription key
subscription_key = "YourSubscriptionKey"

# Create speech translation config from endpoint
config = speechsdk.translation.SpeechTranslationConfig(
    subscription=subscription_key,
    endpoint=v2_endpoint_string
)

# Translation target language and enable personal voice
config.add_target_language("fr")
config.voice_name = "personal-voice"

# You don't need to define any candidate languages to detect.
# Create auto detect source language config from open range
auto_detect_source_language_config = AutoDetectSourceLanguageConfig()

# Example usage:
# You can now use these configurations to create a translation recognizer
# audio_config = speechsdk.audio.AudioConfig(use_default_microphone=True)
# translation_recognizer = speechsdk.translation.TranslationRecognizer(
#     translation_config=config,
#     auto_detect_source_language_config=auto_detect_source_language_config,
#     audio_config=audio_config
# )

print("Azure Live Interpreter configuration created successfully!")
print(f"Endpoint: {v2_endpoint_string}")
print(f"Target language: fr")
print(f"Voice name: personal-voice")
print("Auto language detection: Enabled (open range)")