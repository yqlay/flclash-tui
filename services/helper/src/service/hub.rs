use once_cell::sync::Lazy;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::VecDeque;
use std::fs::File;
use std::io::{BufRead, Error, Read};
use std::process::{Command, Stdio};
use std::sync::{Arc, Mutex};
use std::{io, thread};
use warp::{Filter, Reply};

const LISTEN_PORT: u16 = 47890;

#[derive(Debug, Deserialize, Serialize, Clone)]
pub struct StartParams {
    pub path: String,
    pub arg: String,
}

fn sha256_file(path: &str) -> Result<String, Error> {
    let mut file = File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buffer = [0; 4096];

    loop {
        let bytes_read = file.read(&mut buffer)?;
        if bytes_read == 0 {
            break;
        }
        hasher.update(&buffer[..bytes_read]);
    }

    Ok(format!("{:x}", hasher.finalize()))
}

static LOGS: Lazy<Arc<Mutex<VecDeque<String>>>> =
    Lazy::new(|| Arc::new(Mutex::new(VecDeque::with_capacity(100))));
static PROCESS: Lazy<Arc<Mutex<Option<std::process::Child>>>> =
    Lazy::new(|| Arc::new(Mutex::new(None)));

fn start(start_params: StartParams) -> impl Reply {
    if !cfg!(debug_assertions) {
        if let Err(error) = verify_executable(start_params.path.as_str(), env!("TOKEN")) {
            return error;
        }
    }
    stop();
    let mut process = PROCESS.lock().unwrap();
    match Command::new(&start_params.path)
        .stderr(Stdio::piped())
        .arg(&start_params.arg)
        .spawn()
    {
        Ok(child) => {
            *process = Some(child);
            if let Some(ref mut child) = *process {
                let stderr = child.stderr.take().unwrap();
                let reader = io::BufReader::new(stderr);
                thread::spawn(move || {
                    for line in reader.lines() {
                        match line {
                            Ok(output) => {
                                log_message(output);
                            }
                            Err(_) => {
                                break;
                            }
                        }
                    }
                });
            }
            "".to_string()
        }
        Err(e) => {
            log_message(e.to_string());
            e.to_string()
        }
    }
}

fn verify_executable(path: &str, expected_sha256: &str) -> Result<(), String> {
    if expected_sha256.is_empty() {
        return Err("The helper was built without an executable authorization token.".to_string());
    }
    let actual_sha256 = sha256_file(path)
        .map_err(|error| format!("Unable to verify the requesting executable: {error}"))?;
    if actual_sha256 != expected_sha256 {
        return Err("The requesting executable is not authorized by this helper.".to_string());
    }
    Ok(())
}

fn stop() -> impl Reply {
    let mut process = PROCESS.lock().unwrap();
    if let Some(mut child) = process.take() {
        let _ = child.kill();
        let _ = child.wait();
    }
    *process = None;
    "".to_string()
}

fn log_message(message: String) {
    let mut log_buffer = LOGS.lock().unwrap();
    if log_buffer.len() == 100 {
        log_buffer.pop_front();
    }
    log_buffer.push_back(format!("{}\n", message));
}

fn get_logs() -> impl Reply {
    let log_buffer = LOGS.lock().unwrap();
    let value = log_buffer
        .iter()
        .cloned()
        .collect::<Vec<String>>()
        .join("\n");
    warp::reply::with_header(value, "Content-Type", "text/plain")
}

pub async fn run_service() -> anyhow::Result<()> {
    let api_ping = warp::get().and(warp::path("ping")).map(|| env!("TOKEN"));

    let api_start = warp::post()
        .and(warp::path("start"))
        .and(warp::body::json())
        .map(|start_params: StartParams| start(start_params));

    let api_stop = warp::post().and(warp::path("stop")).map(stop);

    let api_logs = warp::get().and(warp::path("logs")).map(get_logs);

    warp::serve(api_ping.or(api_start).or(api_stop).or(api_logs))
        .run(([127, 0, 0, 1], LISTEN_PORT))
        .await;

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::{sha256_file, verify_executable};
    use std::fs;
    use std::time::{SystemTime, UNIX_EPOCH};

    fn temporary_file() -> std::path::PathBuf {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system time must be after the Unix epoch")
            .as_nanos();
        std::env::temp_dir().join(format!("flclash-helper-{}-{nonce}", std::process::id()))
    }

    #[test]
    fn verification_rejects_an_empty_build_token() {
        let path = temporary_file();
        fs::write(&path, b"trusted executable").expect("write test executable");
        let result = verify_executable(path.to_str().expect("UTF-8 test path"), "");
        fs::remove_file(path).expect("remove test executable");
        assert!(result.is_err());
    }

    #[test]
    fn verification_rejects_missing_and_mismatched_executables() {
        let missing = temporary_file();
        assert!(verify_executable(missing.to_str().expect("UTF-8 test path"), "expected").is_err());

        fs::write(&missing, b"untrusted executable").expect("write test executable");
        let result = verify_executable(missing.to_str().expect("UTF-8 test path"), "expected");
        fs::remove_file(missing).expect("remove test executable");
        assert!(result.is_err());
    }

    #[test]
    fn verification_accepts_the_exact_executable_hash() {
        let path = temporary_file();
        fs::write(&path, b"trusted executable").expect("write test executable");
        let path_string = path.to_str().expect("UTF-8 test path");
        let expected = sha256_file(path_string).expect("hash test executable");
        let result = verify_executable(path_string, &expected);
        fs::remove_file(path).expect("remove test executable");
        assert!(result.is_ok());
    }
}
